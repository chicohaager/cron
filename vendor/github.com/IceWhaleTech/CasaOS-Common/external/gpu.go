package external

import (
	"os/exec"
	"strconv"
	"strings"
)

type NvidiaGPUInfo struct {
	Index         int
	UUID          string
	DriverVersion string
	Name          string
	GPUSerial     string
}

func NvidiaGPUInfoListWithSMI() ([]NvidiaGPUInfo, error) {
	GPUInfos := []NvidiaGPUInfo{}

	output, err := exec.Command("nvidia-smi", "--query-gpu=index,uuid,driver_version,name,gpu_serial", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		value := strings.Split(line, ", ")
		if len(value) == 5 {
			index, _ := strconv.Atoi(value[0])

			GPUInfos = append(GPUInfos, NvidiaGPUInfo{
				Index:         index,
				UUID:          value[1],
				DriverVersion: value[2],
				Name:          value[3],
				GPUSerial:     value[4],
			})
		} else {
			continue
		}
	}
	return GPUInfos, nil
}

func NvidiaGPUInfoList() ([]NvidiaGPUInfo, error) {
	gpusInfo, err := NvidiaGPUInfoListWithSMI()
	if err != nil {
		return nil, err
	}
	return gpusInfo, nil
}
