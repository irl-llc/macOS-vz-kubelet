package resourcemanager

// PodNetworkName returns a deterministic vmnet network name for a pod.
// Format: "vk-<first 12 chars of UID>" — valid as a CLI network identifier.
func PodNetworkName(podUID string) string {
	uid := podUID
	if len(uid) > 12 {
		uid = uid[:12]
	}
	return "vk-" + uid
}
