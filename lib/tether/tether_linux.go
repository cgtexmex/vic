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
	"fmt"
	"io"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/kr/pty"
	"github.com/vmware/vic/pkg/trace"
)

// Mkdev will hopefully get rolled into go.sys at some point
func Mkdev(majorNumber int, minorNumber int) int {
	return (majorNumber << 8) | (minorNumber & 0xff) | ((minorNumber & 0xfff00) << 12)
}

// childReaper is used to handle events from child processes, including child exit.
// If running as pid=1 then this means it handles zombie process reaping for orphaned children
// as well as direct child processes.
func (t *tether) childReaper() {
	signal.Notify(t.incoming, syscall.SIGCHLD)

	// TODO: Call prctl with PR_SET_CHILD_SUBREAPER so that we reap regardless of pid 1 or not
	// we already get our direct children, but not lower in the hierarchy

	log.Info("Started reaping child processes")

	go func() {
		for _ = range t.incoming {
			var status syscall.WaitStatus

			func() {
				// general resiliency
				defer recover()

				// reap until no more children to process
				for {
					log.Debugf("Inspecting children with status change")
					pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
					if pid == 0 || err == syscall.ECHILD {
						log.Debug("No more child processes to reap")
						break
					}
					if err == nil {
						if !status.Exited() {
							log.Debugf("Received notifcation about non-exit status change for %d:", pid)
							// no reaping or exit handling required
							continue
						}

						log.Debugf("Reaped process %d, return code: %d", pid, status.ExitStatus())

						session, ok := t.removeChildPid(pid)
						if ok {
							session.ExitStatus = status.ExitStatus()
							t.handleSessionExit(session)
						} else {
							// This is an adopted zombie. The Wait4 call
							// already clean it up from the kernel
							log.Infof("Reaped zombie process PID %d\n", pid)
						}
					} else {
						log.Warnf("Wait4 got error: %v\n", err)
					}
				}
			}()
		}
	}()
}

func (t *tether) stopReaper() {
	defer trace.End(trace.Begin("Shutting down child reaping"))

	// stop child reaping
	log.Info("Shutting down reaper")
	signal.Reset(syscall.SIGCHLD)
	close(t.incoming)
}

func findExecutable(file string) error {
	d, err := os.Stat(file)
	if err != nil {
		return err
	}
	if m := d.Mode(); !m.IsDir() && m&0111 != 0 {
		return nil
	}
	return os.ErrPermission
}

// lookPath searches for an executable binary named file in the directories
// specified by the path argument.
// This is a direct modification of the unix os/exec core libary impl
func lookPath(file string, env []string, dir string) (string, error) {
	// if it starts with a ./ or ../ it's a relative path
	// need to check explicitly to allow execution of .hidden files

	if strings.HasPrefix(file, "./") || strings.HasPrefix(file, "../") {
		file = fmt.Sprintf("%s%c%s", dir, os.PathSeparator, file)
		err := findExecutable(file)
		if err == nil {
			return file, nil
		}
		return "", err
	}

	// check if it's already a path spec
	if strings.Contains(file, "/") {
		err := findExecutable(file)
		if err == nil {
			return file, nil
		}
		return "", err
	}

	// extract path from the env
	var pathenv string
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			pathenv = value
			break
		}
	}

	pathval := strings.TrimPrefix(pathenv, "PATH=")

	dirs := filepath.SplitList(pathval)
	for _, dir := range dirs {
		if dir == "" {
			// Unix shell semantics: path element "" means "."
			dir = "."
		}
		path := dir + "/" + file
		if err := findExecutable(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s: no such executable in PATH", file)
}

func establishPty(session *SessionConfig) error {
	defer trace.End(trace.Begin("initializing pty handling for session " + session.ID))

	// TODO: if we want to allow raw output to the log so that subsequent tty enabled
	// processing receives the control characters then we should be binding the PTY
	// during attach, and using the same path we have for non-tty here
	var err error
	session.Pty, err = pty.Start(&session.Cmd)
	if session.Pty != nil {
		// TODO: do we need to ensure all reads have completed before calling Wait on the process?
		// it frees up all resources - does that mean it frees the output buffers?
		go func() {
			_, gerr := io.Copy(session.Outwriter, session.Pty)
			log.Debug(gerr)
		}()
		go func() {
			_, gerr := io.Copy(session.Pty, session.Reader)
			log.Debug(gerr)
		}()
	}

	return err
}
