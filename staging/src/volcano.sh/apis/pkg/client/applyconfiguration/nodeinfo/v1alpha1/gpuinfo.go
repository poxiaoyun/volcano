package v1alpha1

type GPUInfoApplyConfiguration struct {
	NUMANodeID  *int    `json:"numa,omitempty"`
	BusID       *string `json:"busID,omitempty"`
	DeviceModel *string `json:"deviceModel,omitempty"`
}

func GPUInfo() *GPUInfoApplyConfiguration {
	return &GPUInfoApplyConfiguration{}
}

func (configuration *GPUInfoApplyConfiguration) WithNUMANodeID(value int) *GPUInfoApplyConfiguration {
	configuration.NUMANodeID = &value
	return configuration
}

func (configuration *GPUInfoApplyConfiguration) WithBusID(value string) *GPUInfoApplyConfiguration {
	configuration.BusID = &value
	return configuration
}

func (configuration *GPUInfoApplyConfiguration) WithDeviceModel(value string) *GPUInfoApplyConfiguration {
	configuration.DeviceModel = &value
	return configuration
}
