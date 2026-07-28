package remoteconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

type HostConnectors struct {
	Version        int             `json:"version"`
	MinCoreVersion string          `json:"minCoreVersion"`
	Connectors     []HostConnector `json:"connectors"`
}

type HostConnector struct {
	ID        string    `json:"id"`
	TeamID    string    `json:"teamId"`
	OriginID  string    `json:"originId"`
	Runtime   string    `json:"runtime"`
	Connector Connector `json:"connector"`
}

type HostConnectorsSnapshot struct {
	Config   HostConnectors `json:"config"`
	Revision string         `json:"revision"`
}

type HostConnectorTransactions struct {
	Path string
}

func NewHostConnectorTransactions(path string) *HostConnectorTransactions {
	return &HostConnectorTransactions{Path: path}
}

func LoadHostConnectors(path string) (HostConnectors, error) {
	var result HostConnectors
	if err := load(path, &result, validateHostConnectorsShape); err != nil {
		return HostConnectors{}, err
	}
	if err := result.Validate(); err != nil {
		return HostConnectors{}, fmt.Errorf("validate host-connectors.json: %w", err)
	}
	return result, nil
}

func SaveHostConnectors(path string, value *HostConnectors) error {
	if value == nil {
		return errors.New("host connector registry is nil")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	return save(path, value)
}

func (config HostConnectors) Validate() error {
	if err := validateHeader(config.Version, config.MinCoreVersion); err != nil {
		return err
	}
	if config.Connectors == nil {
		return errors.New("connectors must be an array")
	}
	ids := make(map[string]bool, len(config.Connectors))
	identities := make(map[string]bool, len(config.Connectors))
	for index, connector := range config.Connectors {
		if err := connector.Validate(); err != nil {
			return fmt.Errorf("connectors[%d]: %w", index, err)
		}
		if ids[connector.ID] {
			return fmt.Errorf("duplicate connector id %q", connector.ID)
		}
		identity := connector.TeamID + "\x00" + connector.OriginID
		if identities[identity] {
			return errors.New("duplicate connector team/origin identity")
		}
		ids[connector.ID] = true
		identities[identity] = true
	}
	return nil
}

func (connector HostConnector) Validate() error {
	if err := validateIdentity("id", connector.ID); err != nil {
		return err
	}
	if err := validateIdentity("teamId", connector.TeamID); err != nil {
		return err
	}
	if err := validateIdentity("originId", connector.OriginID); err != nil {
		return err
	}
	if connector.Runtime != "wsl" &&
		connector.Runtime != "ssh" &&
		connector.Runtime != "container" {
		return fmt.Errorf("unsupported host connector runtime %q", connector.Runtime)
	}
	if connector.Connector.Type != connector.Runtime {
		return errors.New("connector type must match runtime")
	}
	if connector.Connector.HTTPS != nil ||
		connector.Connector.VendorCloud != nil {
		return errors.New("host registry does not support push connectors")
	}
	return connector.Connector.Validate(connector.Runtime)
}

func (transactions *HostConnectorTransactions) List(
	ctx context.Context,
) (HostConnectorsSnapshot, error) {
	if err := transactionContext(ctx); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	value, err := LoadHostConnectors(transactions.Path)
	if err != nil {
		return HostConnectorsSnapshot{}, err
	}
	return hostConnectorSnapshot(value), nil
}

func (transactions *HostConnectorTransactions) Initialize(
	ctx context.Context,
	value HostConnectors,
	dryRun bool,
) (HostConnectorsSnapshot, error) {
	if err := transactionContext(ctx); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	release, err := NewRelayTransactions(transactions.Path).acquire(ctx)
	if err != nil {
		return HostConnectorsSnapshot{}, err
	}
	defer release()
	if _, err := os.Lstat(transactions.Path); err == nil {
		return HostConnectorsSnapshot{}, errors.New(
			"host connector registry already exists",
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return HostConnectorsSnapshot{}, err
	}
	if err := value.Validate(); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	snapshot := hostConnectorSnapshot(value)
	if !dryRun {
		if err := SaveHostConnectors(transactions.Path, &value); err != nil {
			return HostConnectorsSnapshot{}, err
		}
	}
	return snapshot, nil
}

func (transactions *HostConnectorTransactions) Add(
	ctx context.Context,
	connector HostConnector,
	expectedRevision string,
	dryRun bool,
) (HostConnectorsSnapshot, error) {
	return transactions.mutate(ctx, expectedRevision, dryRun, true, func(
		value *HostConnectors,
	) error {
		if err := connector.Validate(); err != nil {
			return err
		}
		for _, existing := range value.Connectors {
			if existing.ID == connector.ID {
				return errors.New("host connector id already exists")
			}
			if existing.TeamID == connector.TeamID &&
				existing.OriginID == connector.OriginID {
				return errors.New("host connector identity already exists")
			}
		}
		value.Connectors = append(value.Connectors, connector)
		sort.Slice(value.Connectors, func(i, j int) bool {
			return value.Connectors[i].ID < value.Connectors[j].ID
		})
		return nil
	})
}

func (transactions *HostConnectorTransactions) Remove(
	ctx context.Context,
	id string,
	expectedRevision string,
	dryRun bool,
) (HostConnectorsSnapshot, error) {
	return transactions.mutate(ctx, expectedRevision, dryRun, false, func(
		value *HostConnectors,
	) error {
		index := -1
		for candidate, connector := range value.Connectors {
			if connector.ID == id {
				index = candidate
				break
			}
		}
		if index < 0 {
			return errors.New("host connector not found")
		}
		value.Connectors = append(
			value.Connectors[:index],
			value.Connectors[index+1:]...,
		)
		return nil
	})
}

func (transactions *HostConnectorTransactions) mutate(
	ctx context.Context,
	expectedRevision string,
	dryRun bool,
	createMissing bool,
	mutation func(*HostConnectors) error,
) (HostConnectorsSnapshot, error) {
	if err := transactionContext(ctx); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	release, err := NewRelayTransactions(transactions.Path).acquire(ctx)
	if err != nil {
		return HostConnectorsSnapshot{}, err
	}
	defer release()
	value, err := LoadHostConnectors(transactions.Path)
	if err != nil {
		if !createMissing || !errors.Is(err, ErrNotFound) {
			return HostConnectorsSnapshot{}, err
		}
		value = HostConnectors{
			Version:        Version,
			MinCoreVersion: "0.3.0",
			Connectors:     []HostConnector{},
		}
	}
	before := hostConnectorSnapshot(value)
	if expectedRevision != "" && before.Revision != expectedRevision {
		return HostConnectorsSnapshot{}, errors.New(
			"host connector registry revision conflict",
		)
	}
	if err := mutation(&value); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	if err := value.Validate(); err != nil {
		return HostConnectorsSnapshot{}, err
	}
	after := hostConnectorSnapshot(value)
	if !dryRun {
		current, loadErr := LoadHostConnectors(transactions.Path)
		if loadErr != nil {
			if !(createMissing && errors.Is(loadErr, ErrNotFound)) {
				return HostConnectorsSnapshot{}, loadErr
			}
			current = HostConnectors{
				Version:        Version,
				MinCoreVersion: "0.3.0",
				Connectors:     []HostConnector{},
			}
		}
		if hostConnectorSnapshot(current).Revision != before.Revision {
			return HostConnectorsSnapshot{}, errors.New(
				"host connector registry revision conflict",
			)
		}
		if err := SaveHostConnectors(transactions.Path, &value); err != nil {
			return HostConnectorsSnapshot{}, err
		}
	}
	return after, nil
}

func hostConnectorSnapshot(value HostConnectors) HostConnectorsSnapshot {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return HostConnectorsSnapshot{
		Config:   value,
		Revision: hex.EncodeToString(digest[:]),
	}
}

func validateHostConnectorsShape(raw []byte) error {
	root, err := rawObject(raw)
	if err != nil {
		return err
	}
	if err := requireFields(root, []string{
		"version",
		"minCoreVersion",
		"connectors",
	}); err != nil {
		return err
	}
	var connectors []map[string]json.RawMessage
	if err := json.Unmarshal(root["connectors"], &connectors); err != nil ||
		connectors == nil {
		return errors.New("field \"connectors\" must be an array")
	}
	for index, connector := range connectors {
		if connector == nil {
			return fmt.Errorf("connectors[%d] must be an object", index)
		}
		if err := requireFields(connector, []string{
			"id",
			"teamId",
			"originId",
			"runtime",
			"connector",
		}); err != nil {
			return fmt.Errorf("connectors[%d]: %w", index, err)
		}
	}
	return nil
}

func transactionContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
