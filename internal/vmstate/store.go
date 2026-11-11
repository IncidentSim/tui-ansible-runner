package vmstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"incidentctl/internal/vm"
)

const defaultFileName = "vm-state.json"

type Store struct {
	path string
	vms  map[string]Record
}

type Record struct {
	VMID string     `json:"vm_id"`
	SSH  *SSHRecord `json:"ssh,omitempty"`
}

type SSHRecord struct {
	Host                  string         `json:"host"`
	Port                  int            `json:"port"`
	User                  string         `json:"user"`
	PrivateKey            string         `json:"private_key"`
	StrictHostKeyChecking bool           `json:"strict_host_key_checking"`
	Bastion               *BastionRecord `json:"bastion,omitempty"`
}

type BastionRecord struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

type fileData struct {
	VMs map[string]Record `json:"vms"`
}

func Load(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = defaultPath()
		if err != nil {
			return nil, err
		}
	}
	store := &Store{path: path, vms: make(map[string]Record)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VM state: %w", err)
	}
	var file fileData
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode VM state: %w", err)
	}
	if file.VMs != nil {
		store.vms = file.VMs
	}
	return store, nil
}

func (s *Store) Lookup(exercise string) (Record, bool) {
	record, ok := s.vms[exercise]
	return record, ok
}

func (s *Store) SaveVM(exercise, vmID string) error {
	if strings.TrimSpace(exercise) == "" {
		return errors.New("exercise name is required")
	}
	if strings.TrimSpace(vmID) == "" {
		return errors.New("VM id is required")
	}
	record := s.vms[exercise]
	record.VMID = vmID
	s.vms[exercise] = record
	return s.persist()
}

func (s *Store) SaveSSH(exercise string, connection vm.Connection) error {
	record, ok := s.vms[exercise]
	if !ok || strings.TrimSpace(record.VMID) == "" {
		return fmt.Errorf("no VM is saved for exercise %q", exercise)
	}
	privateKey := connection.PrivateKey
	if privateKey == "" && connection.IdentityFile != "" {
		data, err := os.ReadFile(connection.IdentityFile)
		if err != nil {
			return fmt.Errorf("read SSH identity file: %w", err)
		}
		privateKey = string(data)
	}
	if strings.TrimSpace(privateKey) == "" {
		return errors.New("SSH private key is empty")
	}
	sshRecord := &SSHRecord{
		Host:                  connection.Host,
		Port:                  connection.Port,
		User:                  connection.User,
		PrivateKey:            privateKey,
		StrictHostKeyChecking: connection.StrictHostKeyChecking,
	}
	if connection.Bastion != nil {
		sshRecord.Bastion = &BastionRecord{
			Host: connection.Bastion.Host,
			Port: connection.Bastion.Port,
			User: connection.Bastion.User,
		}
	}
	record.SSH = sshRecord
	s.vms[exercise] = record
	return s.persist()
}

func (s *Store) OpenConnection(exercise string) (vm.Connection, func(), error) {
	record, ok := s.vms[exercise]
	if !ok || record.SSH == nil {
		return vm.Connection{}, nil, fmt.Errorf("SSH data is not saved for exercise %q", exercise)
	}
	if strings.TrimSpace(record.SSH.PrivateKey) == "" {
		return vm.Connection{}, nil, errors.New("saved SSH private key is empty")
	}
	file, err := os.CreateTemp("", "incidentctl-ssh-state-*")
	if err != nil {
		return vm.Connection{}, nil, fmt.Errorf("create temporary SSH key: %w", err)
	}
	path := file.Name()
	removeKey := func() { _ = os.Remove(path) }
	if err := vm.ProtectSSHKey(file); err != nil {
		_ = file.Close()
		removeKey()
		return vm.Connection{}, nil, fmt.Errorf("protect temporary SSH key: %w", err)
	}
	if _, err := file.WriteString(record.SSH.PrivateKey); err != nil {
		_ = file.Close()
		removeKey()
		return vm.Connection{}, nil, fmt.Errorf("write temporary SSH key: %w", err)
	}
	if err := file.Close(); err != nil {
		removeKey()
		return vm.Connection{}, nil, fmt.Errorf("close temporary SSH key: %w", err)
	}
	connection := vm.Connection{
		Host:                  record.SSH.Host,
		Port:                  record.SSH.Port,
		User:                  record.SSH.User,
		IdentityFile:          path,
		PrivateKey:            record.SSH.PrivateKey,
		StrictHostKeyChecking: record.SSH.StrictHostKeyChecking,
	}
	if record.SSH.Bastion != nil {
		connection.Bastion = &vm.Bastion{
			Host: record.SSH.Bastion.Host,
			Port: record.SSH.Bastion.Port,
			User: record.SSH.Bastion.User,
		}
	}
	return connection, removeKey, nil
}

func (s *Store) Clear() error {
	s.vms = make(map[string]Record)
	return s.persist()
}

func (s *Store) persist() error {
	data, err := json.MarshalIndent(fileData{VMs: s.vms}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode VM state: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create VM state directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".vm-state-*")
	if err != nil {
		return fmt.Errorf("create temporary VM state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := vm.ProtectSSHKey(temporary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary VM state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary VM state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary VM state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("install VM state: %w", err)
	}
	return nil
}

func defaultPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve VM state directory: %w", err)
	}
	return filepath.Join(configDir, "incidentctl", defaultFileName), nil
}
