// Package cache deploys and drives the Data Feeds cache contract
// (contracts/data-feeds/data-feeds-cache) for the local CRE e2e.
package cache

import (
	"context"
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// WorkflowPermission mirrors the contract's WorkflowPermission config type.
type WorkflowPermission struct {
	// AllowedSender is the forwarder contract (C… strkey) reports must arrive through.
	AllowedSender string
	// AllowedWorkflowOwner is the 20-byte workflow owner from report metadata.
	AllowedWorkflowOwner [20]byte
	// AllowedWorkflowName is the 10-byte workflow name from report metadata.
	AllowedWorkflowName [10]byte
}

// FeedConfigEntry mirrors the contract's FeedConfigEntry admin type.
type FeedConfigEntry struct {
	DataID      [16]byte
	Description string
	Permissions []WorkflowPermission
}

// RoundData mirrors the contract's RoundData read type.
type RoundData struct {
	RoundID   uint64
	Answer    *big.Int
	Timestamp uint64
	LedgerSeq uint32
	Primary   bool
}

// DeployCache deploys the Data Feeds cache from compiled WASM bytes, passing the
// owner to the contract's __constructor.
func DeployCache(ctx context.Context, deployer *deployment.Deployer, wasm []byte, salt [32]byte, owner string) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	if len(wasm) == 0 {
		return "", fmt.Errorf("cache wasm is empty")
	}
	if owner == "" {
		return "", fmt.Errorf("owner is required")
	}
	contractID, err := deployer.DeployContractBytesWithConstructor(ctx, wasm, salt, []xdr.ScVal{
		scval.AddressToScVal(owner),
	})
	if err != nil {
		return "", fmt.Errorf("deploy data feeds cache wasm: %w", err)
	}
	return contractID, nil
}

// AddFeedAdmin registers admin as a feed admin (owner-authorized call; the
// deployer's source account must be the contract owner).
func AddFeedAdmin(ctx context.Context, deployer *deployment.Deployer, contractID, admin string) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	_, err := deployer.InvokeContract(ctx, contractID, "add_feed_admin", []xdr.ScVal{
		scval.AddressToScVal(admin),
	})
	if err != nil {
		return fmt.Errorf("add_feed_admin on %s: %w", contractID, err)
	}
	return nil
}

// SetFeedConfigs registers feed configs (admin-authorized call; the deployer's
// source account must be the given admin).
func SetFeedConfigs(ctx context.Context, deployer *deployment.Deployer, contractID, admin string, entries []FeedConfigEntry) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	if len(entries) == 0 {
		return fmt.Errorf("at least one feed config entry is required")
	}
	entryVals := make([]xdr.ScVal, 0, len(entries))
	for _, e := range entries {
		v, err := feedConfigEntryToScVal(e)
		if err != nil {
			return fmt.Errorf("encode feed config entry: %w", err)
		}
		entryVals = append(entryVals, v)
	}
	_, err := deployer.InvokeContract(ctx, contractID, "set_feed_configs", []xdr.ScVal{
		scval.AddressToScVal(admin),
		scval.VecToScVal(entryVals),
	})
	if err != nil {
		return fmt.Errorf("set_feed_configs on %s: %w", contractID, err)
	}
	return nil
}

// LatestRound reads the latest recorded round for a feed; nil when the feed has
// no rounds yet.
func LatestRound(ctx context.Context, deployer *deployment.Deployer, contractID string, dataID [16]byte) (*RoundData, error) {
	if deployer == nil {
		return nil, fmt.Errorf("deployer is nil")
	}
	sv, err := deployer.SimulateContract(ctx, contractID, "latest_round", []xdr.ScVal{
		scval.Bytes16ToScVal(dataID),
	})
	if err != nil {
		return nil, fmt.Errorf("simulate latest_round on %s: %w", contractID, err)
	}
	if sv == nil || sv.Type == xdr.ScValTypeScvVoid {
		return nil, nil
	}
	return roundDataFromScVal(*sv)
}

func feedConfigEntryToScVal(e FeedConfigEntry) (xdr.ScVal, error) {
	perms := make([]xdr.ScVal, 0, len(e.Permissions))
	for _, p := range e.Permissions {
		pv, err := scval.BuildStructScVal(map[string]xdr.ScVal{
			"allowed_sender":         scval.AddressToScVal(p.AllowedSender),
			"allowed_workflow_owner": scval.BytesToScVal(p.AllowedWorkflowOwner[:]),
			"allowed_workflow_name":  scval.BytesToScVal(p.AllowedWorkflowName[:]),
		})
		if err != nil {
			return xdr.ScVal{}, err
		}
		perms = append(perms, pv)
	}
	config, err := scval.BuildStructScVal(map[string]xdr.ScVal{
		"description":          scval.StringToScVal(e.Description),
		"workflow_permissions": scval.VecToScVal(perms),
	})
	if err != nil {
		return xdr.ScVal{}, err
	}
	return scval.BuildStructScVal(map[string]xdr.ScVal{
		"data_id": scval.Bytes16ToScVal(e.DataID),
		"config":  config,
	})
}

func roundDataFromScVal(sv xdr.ScVal) (*RoundData, error) {
	m, ok := sv.GetMap()
	if !ok || m == nil {
		return nil, fmt.Errorf("latest_round did not return a struct (got scval type %v)", sv.Type)
	}
	out := &RoundData{}
	for _, entry := range *m {
		key, err := scval.SymbolFromScVal(entry.Key)
		if err != nil {
			return nil, fmt.Errorf("round data key: %w", err)
		}
		switch key {
		case "round_id":
			out.RoundID, err = scval.Uint64FromScVal(entry.Val)
		case "answer":
			out.Answer, err = i256FromScVal(entry.Val)
		case "timestamp":
			out.Timestamp, err = scval.Uint64FromScVal(entry.Val)
		case "ledger_seq":
			out.LedgerSeq, err = scval.Uint32FromScVal(entry.Val)
		case "primary":
			b, has := entry.Val.GetB()
			if !has {
				err = fmt.Errorf("primary is not a bool")
			}
			out.Primary = b
		default:
			err = fmt.Errorf("unexpected round data field %q", key)
		}
		if err != nil {
			return nil, fmt.Errorf("round data field %q: %w", key, err)
		}
	}
	if out.Answer == nil {
		return nil, fmt.Errorf("round data missing answer")
	}
	return out, nil
}

func i256FromScVal(sv xdr.ScVal) (*big.Int, error) {
	parts, ok := sv.GetI256()
	if !ok {
		return nil, fmt.Errorf("value is not i256 (got scval type %v)", sv.Type)
	}
	// Assemble from the four 64-bit limbs, most significant first.
	v := new(big.Int).SetInt64(int64(parts.HiHi))
	for _, limb := range []uint64{uint64(parts.HiLo), uint64(parts.LoHi), uint64(parts.LoLo)} {
		v.Lsh(v, 64)
		v.Or(v, new(big.Int).SetUint64(limb))
	}
	return v, nil
}
