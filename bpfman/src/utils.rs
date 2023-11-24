// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of bpfman

use std::{os::unix::fs::PermissionsExt, path::Path, str};

use anyhow::{Context, Result};
use log::{debug, info, warn};
use nix::{
    mount::{mount, MsFlags},
    net::if_::if_nametoindex,
};
use tokio::{fs, io::AsyncReadExt};

use crate::errors::BpfmanError;

// The bpfman socket should always allow the same users and members of the same group
// to Read/Write to it.
pub(crate) const SOCK_MODE: u32 = 0o0660;

// Like tokio::fs::read, but with O_NOCTTY set
pub(crate) async fn read<P: AsRef<Path>>(path: P) -> Result<Vec<u8>, BpfmanError> {
    let mut data = vec![];
    tokio::fs::OpenOptions::new()
        .custom_flags(nix::libc::O_NOCTTY)
        .read(true)
        .open(path)
        .await
        .map_err(|e| BpfmanError::Error(format!("can't open file: {e}")))?
        .read_to_end(&mut data)
        .await
        .map_err(|e| BpfmanError::Error(format!("can't read file: {e}")))?;
    Ok(data)
}

// Like tokio::fs::read_to_string, but with O_NOCTTY set
pub(crate) async fn read_to_string<P: AsRef<Path>>(path: P) -> Result<String, BpfmanError> {
    let mut buffer = String::new();
    tokio::fs::OpenOptions::new()
        .custom_flags(nix::libc::O_NOCTTY)
        .read(true)
        .open(path)
        .await
        .map_err(|e| BpfmanError::Error(format!("can't open file: {e}")))?
        .read_to_string(&mut buffer)
        .await
        .map_err(|e| BpfmanError::Error(format!("can't read file: {e}")))?;
    Ok(buffer)
}

pub(crate) fn get_ifindex(iface: &str) -> Result<u32, BpfmanError> {
    match if_nametoindex(iface) {
        Ok(index) => {
            debug!("Map {} to {}", iface, index);
            Ok(index)
        }
        Err(_) => {
            info!("Unable to validate interface {}", iface);
            Err(BpfmanError::InvalidInterface)
        }
    }
}

pub(crate) async fn set_file_permissions(path: &str, mode: u32) {
    // Set the permissions on the file based on input
    if (tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(mode)).await).is_err() {
        warn!("Unable to set permissions on file {}. Continuing", path);
    }
}

pub(crate) async fn set_dir_permissions(directory: &str, mode: u32) {
    // Iterate through the files in the provided directory
    let mut entries = fs::read_dir(directory).await.unwrap();
    while let Some(file) = entries.next_entry().await.unwrap() {
        // Set the permissions on the file based on input
        set_file_permissions(&file.path().into_os_string().into_string().unwrap(), mode).await;
    }
}

pub(crate) fn create_bpffs(directory: &str) -> anyhow::Result<()> {
    debug!("Creating bpffs at {directory}");
    let flags = MsFlags::MS_NOSUID | MsFlags::MS_NODEV | MsFlags::MS_NOEXEC | MsFlags::MS_RELATIME;
    mount::<str, str, str, str>(None, directory, Some("bpf"), flags, None)
        .with_context(|| format!("unable to create bpffs at {directory}"))
}

pub(crate) fn should_map_be_pinned(name: &str) -> bool {
    !(name.contains(".rodata") || name.contains(".bss") || name.contains(".data"))
}