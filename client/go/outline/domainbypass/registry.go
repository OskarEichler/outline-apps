// Copyright 2026 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package domainbypass

import (
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
)

const wildcardLimit = 4096

// Each normalized hostname can occupy 253 bytes plus JSON delimiters.
const maxRegistryBytes = wildcardLimit*256 + 2

type wildcardRegistry struct{ path string }

func wildcardAddress(index int) netip.Addr {
	slot := 32769 + index // Separate from the existing exact-name registry.
	return netip.AddrFrom4([4]byte{198, 18, byte(slot >> 8), byte(slot)})
}

func openWildcardRegistry(dataDir string) (*wildcardRegistry, []string, error) {
	if dataDir == "" {
		return nil, nil, errors.New("missing registry directory")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, nil, err
	}
	r := &wildcardRegistry{filepath.Join(dataDir, "wildcard-domains.json")}
	var hosts []string
	err := r.locked(func() error { var err error; hosts, err = r.read(); return err })
	return r, hosts, err
}

func (r *wildcardRegistry) locked(fn func() error) error {
	f, err := os.OpenFile(r.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockRegistry(f); err != nil {
		return err
	}
	defer unlockRegistry(f)
	return fn()
}

func (r *wildcardRegistry) read() ([]string, error) {
	f, err := os.Open(r.path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRegistryBytes+1))
	if err != nil || len(data) > maxRegistryBytes {
		return nil, errors.New("invalid domain registry")
	}
	var hosts []string
	if json.Unmarshal(data, &hosts) != nil || len(hosts) > wildcardLimit {
		return nil, errors.New("invalid domain registry")
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		normalized, err := normalize(host)
		if err != nil || normalized != host || seen[host] {
			return nil, errors.New("invalid domain registry")
		}
		seen[host] = true
	}
	return hosts, nil
}

func (r *wildcardRegistry) reserve(host string) (netip.Addr, error) {
	var ip netip.Addr
	err := r.locked(func() error {
		hosts, err := r.read()
		if err != nil {
			return err
		}
		for i, known := range hosts {
			if known == host {
				ip = wildcardAddress(i)
				return nil
			}
		}
		if len(hosts) >= wildcardLimit {
			return errors.New("wildcard address registry is full")
		}
		normalized, err := normalize(host)
		if err != nil || normalized != host {
			return errors.New("invalid wildcard hostname")
		}
		ip = wildcardAddress(len(hosts))
		hosts = append(hosts, host)
		data, err := json.Marshal(hosts)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(r.path), ".wildcard-domains-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(f.Name(), r.path); err != nil {
			return err
		}
		dir, err := os.Open(filepath.Dir(r.path))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	})
	return ip, err
}
