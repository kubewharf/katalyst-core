/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package types

type MalachitePowerResponse struct {
	Status int       `json:"status"`
	Data   PowerData `json:"data"`
}

type PowerData struct {
	Sensors SensorData `json:"sensors"`
}

type SensorData struct {
	TotalPowerWatt float64 `json:"total_power"`
	CPUPower       float64 `json:"cpu_power"`
	MemPower       float64 `json:"mem_power"`
	FanPower       float64 `json:"fan_power"`
	HDDPower       float64 `json:"hdd_power"`
	PSU0POut       float64 `json:"psu0_pout"`
	PSU1POut       float64 `json:"psu1_pout"`
	PSU0PIn        float64 `json:"psu0_pin"`
	PSU1PIn        float64 `json:"psu1_pin"`
	UpdateTime     int64   `json:"update_time"`
}
