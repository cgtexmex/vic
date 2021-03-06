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

package tether

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"testing"

	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/context"

	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/vic/lib/metadata"
	"github.com/vmware/vic/pkg/dio"
	"github.com/vmware/vic/pkg/trace"
	"github.com/vmware/vic/pkg/vsphere/extraconfig"
)

var Mocked Mocker

type Mocker struct {
	Base BaseOperations

	// allow tests to tell when the tether has finished setup
	Started chan bool
	// allow tests to tell when the tether has finished
	Cleaned chan bool

	// debug output gets logged here
	LogBuffer bytes.Buffer

	// session output gets logged here
	SessionLogBuffer bytes.Buffer

	// the hostname of the system
	Hostname string
	Aliases  []string
	// the maximum slot number returned from this mocker
	maxSlot int
	// the interfaces in the system indexed by name
	Interfaces map[string]netlink.Link
	// filesystem mounts, indexed by disk label
	Mounts map[string]string

	WindowCol uint32
	WindowRow uint32
	Signal    ssh.Signal
}

// Start implements the extension method
func (t *Mocker) Start() error {
	// TODO: enabled for initial dev debugging only
	go func() {
		log.Info(http.ListenAndServe("0.0.0.0:6060", nil))
	}()

	return nil
}

// Stop implements the extension method
func (t *Mocker) Stop() error {
	close(t.Cleaned)
	return nil
}

// Reload implements the extension method
func (t *Mocker) Reload(config *ExecutorConfig) error {
	// the tether has definitely finished it's startup by the time we hit this
	close(t.Started)
	return nil
}

func (t *Mocker) Setup() error {
	return nil
}

func (t *Mocker) Cleanup() error {
	return nil
}

func (t *Mocker) Log() (io.Writer, error) {
	return &t.LogBuffer, nil
}

func (t *Mocker) SessionLog(session *SessionConfig) (dio.DynamicMultiWriter, error) {
	return dio.MultiWriter(&t.SessionLogBuffer, os.Stdout), nil
}

func (t *Mocker) HandleSessionExit(config *ExecutorConfig, session *SessionConfig) bool {
	// check for executor behaviour
	return session.ID == config.ID
}

func (t *Mocker) ProcessEnv(env []string) []string {
	return t.Base.ProcessEnv(env)
}

// SetHostname sets both the kernel hostname and /etc/hostname to the specified string
func (t *Mocker) SetHostname(hostname string, aliases ...string) error {
	defer trace.End(trace.Begin("mocking hostname to " + hostname))

	// TODO: we could mock at a much finer granularity, only extracting the syscall
	// that would exercise the file modification paths, however it's much less generalizable
	t.Hostname = hostname
	t.Aliases = aliases
	return nil
}

// Apply takes the network endpoint configuration and applies it to the system
func (t *Mocker) Apply(endpoint *metadata.NetworkEndpoint) error {
	return apply(t, endpoint)
}

// MountLabel performs a mount with the source treated as a disk label
// This assumes that /dev/disk/by-label is being populated, probably by udev
func (t *Mocker) MountLabel(label, target string, ctx context.Context) error {
	defer trace.End(trace.Begin(fmt.Sprintf("mocking mounting %s on %s", label, target)))

	if t.Mounts == nil {
		t.Mounts = make(map[string]string)
	}

	t.Mounts[label] = target
	return nil
}

// Fork triggers vmfork and handles the necessary pre/post OS level operations
func (t *Mocker) Fork() error {
	defer trace.End(trace.Begin("mocking fork"))
	return errors.New("Fork test not implemented")
}

// TestMain simply so we have control of debugging level and somewhere to call package wide test setup
func TestMain(m *testing.M) {
	log.SetLevel(log.DebugLevel)
	trace.Logger = log.StandardLogger()

	retCode := m.Run()

	// call with result of m.Run()
	os.Exit(retCode)
}

func StartTether(t *testing.T, cfg *metadata.ExecutorConfig) (Tether, extraconfig.DataSource) {
	store := map[string]string{}
	sink := extraconfig.MapSink(store)
	src := extraconfig.MapSource(store)
	extraconfig.Encode(sink, cfg)
	log.Debugf("Test configuration: %#v", sink)

	tthr := New(src, sink, &Mocked)
	tthr.Register("mocker", &Mocked)

	// run the tether to service the attach
	go func() {
		erR := tthr.Start()
		if erR != nil {
			t.Error(erR)
		}
	}()

	return tthr, src
}

func RunTether(t *testing.T, cfg *metadata.ExecutorConfig) (Tether, extraconfig.DataSource, error) {
	store := map[string]string{}
	sink := extraconfig.MapSink(store)
	src := extraconfig.MapSource(store)
	extraconfig.Encode(sink, cfg)
	log.Debugf("Test configuration: %#v", sink)

	tthr := New(src, sink, &Mocked)
	tthr.Register("Mocker", &Mocked)

	// run the tether to service the attach
	erR := tthr.Start()

	return tthr, src, erR
}

func OptionValueArrayToString(options []types.BaseOptionValue) string {
	// create the key/value store from the extraconfig slice for lookups
	kv := make(map[string]string)
	for i := range options {
		k := options[i].GetOptionValue().Key
		v := options[i].GetOptionValue().Value.(string)
		kv[k] = v
	}

	return fmt.Sprintf("%#v", kv)
}

func testSetup(t *testing.T) {
	pc, _, _, _ := runtime.Caller(1)
	name := runtime.FuncForPC(pc).Name()

	log.Infof("Started test setup for %s", name)

	// use the mock ops - fresh one each time as tests might apply different mocked calls
	Mocked = Mocker{
		Started:    make(chan bool, 0),
		Cleaned:    make(chan bool, 0),
		Interfaces: make(map[string]netlink.Link, 0),
	}
}

func testTeardown(t *testing.T) {
	// cleanup
	// os.RemoveAll(pathPrefix)
	log.SetOutput(os.Stdout)

	<-Mocked.Cleaned

	pc, _, _, _ := runtime.Caller(1)
	name := runtime.FuncForPC(pc).Name()

	log.Infof("Finished test teardown for %s", name)
}
