/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use
this file except in compliance with the License. You may obtain a copy of the
License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package bpfmanagent

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	bpfmaniov1alpha1 "github.com/bpfman/bpfman/bpfman-operator/apis/v1alpha1"
	"github.com/bpfman/bpfman/bpfman-operator/internal"
	"github.com/buger/jsonparser"
	"github.com/go-logr/logr"
)

// getPodsForNode returns a list of pods on the given node that match the given
// container selector.
func getPodsForNode(ctx context.Context, clientset kubernetes.Interface,
	containerSelector *bpfmaniov1alpha1.ContainerSelector, nodeName string) (*v1.PodList, error) {

	selectorString := metav1.FormatLabelSelector(&containerSelector.Pods)

	if selectorString == "<error>" {
		return nil, fmt.Errorf("error parsing selector: %v", selectorString)
	}

	listOptions := metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	}

	if selectorString != "<none>" {
		listOptions.LabelSelector = selectorString
	}

	podList, err := clientset.CoreV1().Pods(containerSelector.Namespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("error getting pod list: %v", err)
	}

	return podList, nil
}

type containerInfo struct {
	podName       string
	containerName string
	pid           int64
}

// getContainerInfo returns a list of containerInfo for the given pod list and container names.
func getContainerInfo(podList *v1.PodList, containerNames *[]string, logger logr.Logger) (*[]containerInfo, error) {

	crictl := "/usr/local/bin/crictl"

	containers := []containerInfo{}

	for i, pod := range podList.Items {
		logger.V(1).Info("Pod", "index", i, "Name", pod.Name, "Namespace", pod.Namespace, "NodeName", pod.Spec.NodeName)

		// Find the unique Pod ID of the given pod.
		cmd := exec.Command(crictl, "pods", "--name", pod.Name, "-o", "json")
		podInfo, err := cmd.Output()
		if err != nil {
			logger.Info("Failed to get pod info", "error", err)
			return nil, err
		}

		// The crictl --name option works like a grep on the names of pods.
		// Since we are using the unique name of the pod generated by k8s, we
		// will most likely only get one pod. Though very unlikely, it is
		// technically possible that this unique name is a substring of another
		// pod name. If that happens, we would get multiple pods, so we handle
		// that possibility with the following for loop.
		var podId string
		podFound := false
		for podIndex := 0; ; podIndex++ {
			indexString := "[" + strconv.Itoa(podIndex) + "]"
			podId, err = jsonparser.GetString(podInfo, "items", indexString, "id")
			if err != nil {
				// We hit the end of the list of pods and didn't find it.  This
				// should only happen if the pod was deleted between the time we
				// got the list of pods and the time we got the info about the
				// pod.
				break
			}
			podName, err := jsonparser.GetString(podInfo, "items", indexString, "metadata", "name")
			if err != nil {
				// We shouldn't get an error here if we didn't get an error
				// above, but just in case...
				logger.Error(err, "Error getting pod name")
				break
			}

			if podName == pod.Name {
				podFound = true
				break
			}
		}

		if !podFound {
			return nil, fmt.Errorf("pod %s not found in crictl pod list", pod.Name)
		}

		logger.V(1).Info("podFound", "podId", podId, "err", err)

		// Get info about the containers in the pod so we can get their unique IDs.
		cmd = exec.Command(crictl, "ps", "--pod", podId, "-o", "json")
		containerData, err := cmd.Output()
		if err != nil {
			logger.Info("Failed to get container info", "error", err)
			return nil, err
		}

		// For each container in the pod...
		for containerIndex := 0; ; containerIndex++ {

			indexString := "[" + strconv.Itoa(containerIndex) + "]"

			// Make sure the container name is in the list of containers we want.
			containerName, err := jsonparser.GetString(containerData, "containers", indexString, "metadata", "name")
			if err != nil {
				break
			}

			if containerNames != nil &&
				len(*containerNames) > 0 &&
				!slices.Contains((*containerNames), containerName) {
				continue
			}

			// If it is in the list, get the container ID.
			containerId, err := jsonparser.GetString(containerData, "containers", indexString, "id")
			if err != nil {
				break
			}

			// Now use the container ID to get more info about the container so
			// we can get the PID.
			cmd = exec.Command(crictl, "inspect", "-o", "json", containerId)
			containerData, err := cmd.Output()
			if err != nil {
				logger.Info("Failed to get container data", "error", err)
				continue
			}
			containerPid, err := jsonparser.GetInt(containerData, "info", "pid")
			if err != nil {
				logger.Info("Failed to get container PID", "error", err)
				continue
			}

			container := containerInfo{
				podName:       pod.Name,
				containerName: containerName,
				pid:           containerPid,
			}

			containers = append(containers, container)
		}

	}

	return &containers, nil
}

// Check if the annotation is set to indicate that no containers on this node
// matched the container selector.
func noContainersOnNode(bpfProgram *bpfmaniov1alpha1.BpfProgram) bool {
	if bpfProgram == nil {
		return false
	}

	noContainersOnNode, ok := bpfProgram.Annotations[internal.UprobeNoContainersOnNode]
	if ok && noContainersOnNode == "true" {
		return true
	}

	return false
}
