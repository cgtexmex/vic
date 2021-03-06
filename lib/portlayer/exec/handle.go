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

package exec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"

	log "github.com/Sirupsen/logrus"
	"github.com/golang/groupcache/lru"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/vic/lib/guest"
	"github.com/vmware/vic/lib/metadata"
	"github.com/vmware/vic/lib/spec"
	"github.com/vmware/vic/pkg/trace"
	"github.com/vmware/vic/pkg/vsphere/extraconfig"
	"github.com/vmware/vic/pkg/vsphere/session"
)

const (
	serialOverLANPort  = 2377
	managementHostName = "management.localhost"
)

// ContainerCreateConfig defines the parameters for Create call
type ContainerCreateConfig struct {
	Metadata metadata.ExecutorConfig

	ParentImageID  string
	ImageStoreName string
	VCHName        string
}

var handles *lru.Cache
var handlesLock sync.Mutex

const handleLen = 16
const lruSize = 1000

func init() {
	handles = lru.New(lruSize)
}

type Handle struct {
	Spec       *spec.VirtualMachineConfigSpec
	ExecConfig metadata.ExecutorConfig
	Container  *Container
	State      *State

	key       string
	committed bool
}

func newHandleKey() string {
	b := make([]byte, handleLen)
	rand.Read(b)
	return hex.EncodeToString(b)

}

func newHandle(con *Container) *Handle {
	h := &Handle{
		key:        newHandleKey(),
		committed:  false,
		Container:  con,
		ExecConfig: *con.ExecConfig,
	}

	handlesLock.Lock()
	defer handlesLock.Unlock()

	handles.Add(h.key, h)

	return h
}

func GetHandle(key string) *Handle {
	handlesLock.Lock()
	defer handlesLock.Unlock()

	if h, ok := handles.Get(key); ok {
		return h.(*Handle)
	}

	return nil
}

func removeHandle(key string) {
	handlesLock.Lock()
	defer handlesLock.Unlock()

	handles.Remove(key)
}

func (h *Handle) IsCommitted() bool {
	return h.committed
}

func (h *Handle) SetSpec(s *spec.VirtualMachineConfigSpec) error {
	if h.Spec != nil {
		if s != nil {
			return fmt.Errorf("spec is already set")
		}

		return nil
	}

	if s == nil {
		// initialization
		s = &spec.VirtualMachineConfigSpec{
			VirtualMachineConfigSpec: &types.VirtualMachineConfigSpec{},
		}
	}

	h.Spec = s
	return nil
}

func (h *Handle) String() string {
	return h.key
}

func (h *Handle) Commit(ctx context.Context, sess *session.Session) error {
	if h.committed {
		return nil // already committed
	}

	// make sure there is a spec
	h.SetSpec(nil)
	cfg := make(map[string]string)
	extraconfig.Encode(extraconfig.MapSink(cfg), h.ExecConfig)
	s := h.Spec.Spec()
	s.ExtraConfig = append(s.ExtraConfig, extraconfig.OptionValueFromMap(cfg)...)

	if err := h.Container.Commit(ctx, sess, h); err != nil {
		return err
	}

	h.committed = true
	removeHandle(h.key)
	return nil
}

func (h *Handle) SetState(s State) {
	h.State = new(State)
	*h.State = s
}

func (h *Handle) Create(ctx context.Context, sess *session.Session, config *ContainerCreateConfig) error {
	defer trace.End(trace.Begin("Handle.Create"))

	if h.Spec != nil {
		log.Debugf("spec has already been set on handle %p during create of %s", h, config.Metadata.ID)
		return fmt.Errorf("spec already set")
	}

	// update the handle with Metadata
	h.ExecConfig = config.Metadata
	// add create time to config
	h.ExecConfig.Common.Created = time.Now().UTC().String()
	// Convert the management hostname to IP
	ips, err := net.LookupIP(managementHostName)
	if err != nil {
		log.Errorf("Unable to look up %s during create of %s: %s", managementHostName, config.Metadata.ID, err)
		return err
	}

	if len(ips) == 0 {
		log.Errorf("No IP found for %s during create of %s", managementHostName, config.Metadata.ID)
		return fmt.Errorf("No IP found on %s", managementHostName)
	}

	if len(ips) > 1 {
		log.Errorf("Multiple IPs found for %s during create of %s: %v", managementHostName, config.Metadata.ID, ips)
		return fmt.Errorf("Multiple IPs found on %s: %#v", managementHostName, ips)
	}

	URI := fmt.Sprintf("tcp://%s:%d", ips[0], serialOverLANPort)

	specconfig := &spec.VirtualMachineConfigSpecConfig{
		// FIXME: hardcoded values
		NumCPUs:  2,
		MemoryMB: 2048,

		ConnectorURI: URI,

		ID:   config.Metadata.ID,
		Name: config.Metadata.Name,

		ParentImageID: config.ParentImageID,

		// FIXME: hardcoded value
		BootMediaPath: sess.Datastore.Path(fmt.Sprintf("%s/bootstrap.iso", config.VCHName)),
		VMPathName:    fmt.Sprintf("[%s]", sess.Datastore.Name()),
		NetworkName:   strings.Split(sess.Network.Reference().Value, "-")[1],

		ImageStoreName: config.ImageStoreName,

		Metadata: config.Metadata,
	}
	log.Debugf("Config: %#v", specconfig)

	// Create a linux guest
	linux, err := guest.NewLinuxGuest(ctx, sess, specconfig)
	if err != nil {
		log.Errorf("Failed during linux specific spec generation during create of %s: %s", config.Metadata.ID, err)
		return err
	}

	h.SetSpec(linux.Spec())
	return nil
}
