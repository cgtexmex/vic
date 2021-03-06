// Copyright 2016 VMware, Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package data

import (
	"net"
	"time"

	"github.com/vmware/vic/cmd/vic-machine/common"
)

// Data wrapps all parameters required by value validation
type Data struct {
	*common.Target
	common.Debug

	Insecure bool

	CertPEM []byte
	KeyPEM  []byte

	ComputeResourcePath string
	ImageDatastoreName  string
	DisplayName         string

	ContainerDatastoreName string
	ExternalNetworkName    string
	ManagementNetworkName  string
	BridgeNetworkName      string
	ClientNetworkName      string

	MappedNetworks        map[string]string
	MappedNetworksGateway map[string]*net.IPNet

	NumCPUs  int
	MemoryMB int

	Timeout time.Duration

	Force bool
}

func NewData() *Data {
	d := &Data{
		Target: common.NewTarget(),
	}
	return d
}
