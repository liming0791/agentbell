package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type serviceLock struct {
	path string
	mu   sync.Mutex
}

type lockRecord struct {
	PID       int       `json:"pid"`
	Heartbeat time.Time `json:"heartbeat"`
}

func acquireLock(path string, staleAfter time.Duration) (*serviceLock, error) {
	lock := &serviceLock{path: path}
	if err := lock.create(); err == nil {
		return lock, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) <= staleAfter {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		record := lockRecord{}
		if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
			return nil, fmt.Errorf("decode AgentBell service lock: %w", decodeErr)
		}
		if record.PID <= 0 {
			return nil, errors.New("AgentBell service lock has an invalid PID")
		}
		alive, aliveErr := serviceProcessAlive(record.PID)
		if aliveErr != nil {
			return nil, fmt.Errorf("check AgentBell service lock PID: %w", aliveErr)
		}
		if alive {
			return nil, errors.New("another AgentBell service instance is running")
		}
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove stale service lock: %w", err)
	}
	if err := lock.create(); err != nil {
		return nil, err
	}
	return lock, nil
}

func (lock *serviceLock) create() error {
	file, err := os.OpenFile(lock.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	record := lockRecord{PID: os.Getpid(), Heartbeat: time.Now().UTC()}
	encodeErr := json.NewEncoder(file).Encode(record)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(encodeErr, syncErr, closeErr)
}

func (lock *serviceLock) Heartbeat(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lock.mu.Lock()
			now := time.Now()
			_ = os.Chtimes(lock.path, now, now)
			lock.mu.Unlock()
		}
	}
}

func (lock *serviceLock) Release() error {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
