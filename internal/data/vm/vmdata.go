package vm

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// VirtualMachineData stores the information about macOS virtual machines
type VirtualMachineData struct {
	data sync.Map // map[types.NamespacedName]VirtualMachineInfo (podNamespace/podName -> VirtualMachineInfo)
}

// GetVirtualMachineInfo retrieves the VirtualMachineInfo for a specific pod.
// It returns the VirtualMachineInfo and a boolean indicating whether the virtual machine information was found.
func (d *VirtualMachineData) GetVirtualMachineInfo(podNamespace, podName string) (VirtualMachineInfo, bool) {
	key := types.NamespacedName{Namespace: podNamespace, Name: podName}
	val, ok := d.data.Load(key)
	if !ok {
		return VirtualMachineInfo{}, false
	}
	return *val.(*VirtualMachineInfo), true
}

// UpdateVirtualMachineInfo updates the VirtualMachineInfo for a specific pod.
// It returns the VirtualMachineInfo and a boolean indicating whether the virtual machine information was found.
func (d *VirtualMachineData) UpdateVirtualMachineInfo(podNamespace, podName string, updateFunc func(VirtualMachineInfo) VirtualMachineInfo) (VirtualMachineInfo, bool) {
	key := types.NamespacedName{Namespace: podNamespace, Name: podName}
	val, ok := d.data.Load(key)
	if !ok {
		return VirtualMachineInfo{}, false
	}
	newVal := updateFunc(*val.(*VirtualMachineInfo))
	d.data.Store(key, &newVal)
	return newVal, true
}

// GetOrCreateVirtualMachineInfo retrieves the VirtualMachineInfo for a specific pod,
// or creates and stores the provided VirtualMachineInfo if it doesn't already exist.
// It returns the VirtualMachineInfo and a boolean indicating whether the virtual machine information was already present.
func (d *VirtualMachineData) GetOrCreateVirtualMachineInfo(podNamespace, podName string, info VirtualMachineInfo) (VirtualMachineInfo, bool) {
	key := types.NamespacedName{Namespace: podNamespace, Name: podName}
	val, loaded := d.data.LoadOrStore(key, &info)
	return *val.(*VirtualMachineInfo), loaded
}

// RemoveVirtualMachineInfo removes the VirtualMachineInfo for a specific pod.
func (d *VirtualMachineData) RemoveVirtualMachineInfo(podNamespace, podName string) {
	key := types.NamespacedName{Namespace: podNamespace, Name: podName}
	_, _ = d.data.LoadAndDelete(key)
}

// ListVirtualMachines returns a map of all virtual machines stored.
func (d *VirtualMachineData) ListVirtualMachines() map[types.NamespacedName]VirtualMachineInfo {
	vmMap := make(map[types.NamespacedName]VirtualMachineInfo)
	d.data.Range(func(key, value interface{}) bool {
		vmMap[key.(types.NamespacedName)] = *value.(*VirtualMachineInfo)
		return true
	})
	return vmMap
}
