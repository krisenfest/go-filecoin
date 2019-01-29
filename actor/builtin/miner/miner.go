package miner

import (
	"math/big"
	"strconv"

	"gx/ipfs/QmR8BauakNcBa3RbE4nbQu76PDiJgoQgz8AJdhJuiU4TAw/go-cid"
	cbor "gx/ipfs/QmRoARq3nkUb13HSKZGepCZSWe5GrVPwx7xURJGZ7KWv9V/go-ipld-cbor"
	xerrors "gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmY5Grm8pJdiSSVsYxx4uNRgweY72EmYwuSDbRnbFok3iY/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/exec"
	"github.com/filecoin-project/go-filecoin/proofs"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"
)

func init() {
	cbor.RegisterCborType(State{})
	cbor.RegisterCborType(Ask{})
}

// MaximumPublicKeySize is a limit on how big a public key can be.
const MaximumPublicKeySize = 100

// PoStProofLength is the length of a single proof-of-spacetime proof (in bytes).
const PoStProofLength = 192

// ProvingPeriodBlocks defines how long a proving period is for.
// TODO: what is an actual workable value? currently set very high to avoid race conditions in test.
// https://github.com/filecoin-project/go-filecoin/issues/966
var ProvingPeriodBlocks = types.NewBlockHeight(20000)

const (
	// ErrPublicKeyTooBig indicates an invalid public key.
	ErrPublicKeyTooBig = 33
	// ErrInvalidSector indicates and invalid sector id.
	ErrInvalidSector = 34
	// ErrSectorCommitted indicates the sector has already been committed.
	ErrSectorCommitted = 35
	// ErrStoragemarketCallFailed indicates the call to commit the deal failed.
	ErrStoragemarketCallFailed = 36
	// ErrCallerUnauthorized signals an unauthorized caller.
	ErrCallerUnauthorized = 37
	// ErrInsufficientPledge signals insufficient pledge for what you are trying to do.
	ErrInsufficientPledge = 38
	// ErrInvalidPoSt signals that the passed in PoSt was invalid.
	ErrInvalidPoSt = 39
)

// Errors map error codes to revert errors this actor may return.
var Errors = map[uint8]error{
	ErrPublicKeyTooBig:         errors.NewCodedRevertErrorf(ErrPublicKeyTooBig, "public key must be less than %d bytes", MaximumPublicKeySize),
	ErrInvalidSector:           errors.NewCodedRevertErrorf(ErrInvalidSector, "sectorID out of range"),
	ErrSectorCommitted:         errors.NewCodedRevertErrorf(ErrSectorCommitted, "sector already committed"),
	ErrStoragemarketCallFailed: errors.NewCodedRevertErrorf(ErrStoragemarketCallFailed, "call to StorageMarket failed"),
	ErrCallerUnauthorized:      errors.NewCodedRevertErrorf(ErrCallerUnauthorized, "not authorized to call the method"),
	ErrInsufficientPledge:      errors.NewCodedRevertErrorf(ErrInsufficientPledge, "not enough pledged"),
	ErrInvalidPoSt:             errors.NewCodedRevertErrorf(ErrInvalidPoSt, "PoSt proof did not validate"),
}

// Actor is the miner actor.
type Actor struct{}

// Ask is a price advertisement by the miner
type Ask struct {
	Price  *types.AttoFIL
	Expiry *types.BlockHeight
	ID     *big.Int
}

// State is the miner actors storage.
type State struct {
	Owner address.Address

	// PeerID references the libp2p identity that the miner is operating.
	PeerID peer.ID

	// PublicKey is used to validate blocks generated by the miner this actor represents.
	PublicKey []byte

	// Pledge is amount the space being offered up by this miner.
	PledgeSectors *big.Int

	// Collateral is the total amount of filecoin being held as collateral for
	// the miners pledge.
	Collateral *types.AttoFIL

	// Asks is the set of asks this miner has open
	Asks      []*Ask
	NextAskID *big.Int

	// SectorCommitments maps sector id to commitments, for all sectors this
	// miner has committed. Due to a bug in refmt, the sector id-keys need to be
	// stringified.
	//
	// See also: https://github.com/polydawn/refmt/issues/35
	SectorCommitments map[string]types.Commitments

	LastUsedSectorID uint64

	ProvingPeriodStart *types.BlockHeight
	LastPoSt           *types.BlockHeight

	Power *big.Int
}

// NewActor returns a new miner actor
func NewActor() *actor.Actor {
	return actor.NewActor(types.MinerActorCodeCid, types.NewZeroAttoFIL())
}

// NewState creates a miner state struct
func NewState(owner address.Address, key []byte, pledge *big.Int, pid peer.ID, collateral *types.AttoFIL) *State {
	return &State{
		Owner:             owner,
		PeerID:            pid,
		PublicKey:         key,
		PledgeSectors:     pledge,
		Collateral:        collateral,
		SectorCommitments: make(map[string]types.Commitments),
		Power:             big.NewInt(0),
		NextAskID:         big.NewInt(0),
	}
}

// InitializeState stores this miner's initial data structure.
func (ma *Actor) InitializeState(storage exec.Storage, initializerData interface{}) error {
	minerState, ok := initializerData.(*State)
	if !ok {
		return errors.NewFaultError("Initial state to miner actor is not a miner.State struct")
	}

	// TODO: we should validate this is actually a public key (possibly the owner's public key) once we have a better
	// TODO: idea what crypto looks like.
	if len(minerState.PublicKey) > MaximumPublicKeySize {
		return Errors[ErrPublicKeyTooBig]
	}

	stateBytes, err := cbor.DumpObject(minerState)
	if err != nil {
		return xerrors.Wrap(err, "failed to cbor marshal object")
	}

	id, err := storage.Put(stateBytes)
	if err != nil {
		return err
	}

	return storage.Commit(id, cid.Undef)
}

var _ exec.ExecutableActor = (*Actor)(nil)

var minerExports = exec.Exports{
	"addAsk": &exec.FunctionSignature{
		Params: []abi.Type{abi.AttoFIL, abi.Integer},
		Return: []abi.Type{abi.Integer},
	},
	"getAsks": &exec.FunctionSignature{
		Params: nil,
		Return: []abi.Type{abi.UintArray},
	},
	"getAsk": &exec.FunctionSignature{
		Params: []abi.Type{abi.Integer},
		Return: []abi.Type{abi.Bytes},
	},
	"getOwner": &exec.FunctionSignature{
		Params: nil,
		Return: []abi.Type{abi.Address},
	},
	"getLastUsedSectorID": &exec.FunctionSignature{
		Params: nil,
		Return: []abi.Type{abi.SectorID},
	},
	"commitSector": &exec.FunctionSignature{
		Params: []abi.Type{abi.SectorID, abi.Bytes, abi.Bytes, abi.Bytes, abi.Bytes},
		Return: []abi.Type{},
	},
	"getKey": &exec.FunctionSignature{
		Params: []abi.Type{},
		Return: []abi.Type{abi.Bytes},
	},
	"getPeerID": &exec.FunctionSignature{
		Params: []abi.Type{},
		Return: []abi.Type{abi.PeerID},
	},
	"updatePeerID": &exec.FunctionSignature{
		Params: []abi.Type{abi.PeerID},
		Return: []abi.Type{},
	},
	"getPledge": &exec.FunctionSignature{
		Params: []abi.Type{},
		Return: []abi.Type{abi.Integer},
	},
	"getPower": &exec.FunctionSignature{
		Params: []abi.Type{},
		Return: []abi.Type{abi.Integer},
	},
	"submitPoSt": &exec.FunctionSignature{
		Params: []abi.Type{abi.Bytes},
		Return: []abi.Type{},
	},
	"getProvingPeriodStart": &exec.FunctionSignature{
		Params: []abi.Type{},
		Return: []abi.Type{abi.BlockHeight},
	},
	"getSectorCommitments": &exec.FunctionSignature{
		Params: nil,
		Return: []abi.Type{abi.CommitmentsMap},
	},
}

// Exports returns the miner actors exported functions.
func (ma *Actor) Exports() exec.Exports {
	return minerExports
}

// AddAsk adds an ask to this miners ask list
func (ma *Actor) AddAsk(ctx exec.VMContext, price *types.AttoFIL, expiry *big.Int) (*big.Int, uint8,
	error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		if ctx.Message().From != state.Owner {
			return nil, Errors[ErrCallerUnauthorized]
		}

		id := big.NewInt(0).Set(state.NextAskID)
		state.NextAskID = state.NextAskID.Add(state.NextAskID, big.NewInt(1))

		// filter out expired asks
		asks := state.Asks
		state.Asks = state.Asks[:0]
		for _, a := range asks {
			if ctx.BlockHeight().LessThan(a.Expiry) {
				state.Asks = append(state.Asks, a)
			}
		}

		if !expiry.IsUint64() {
			return nil, errors.NewRevertError("expiry was invalid")
		}
		expiryBH := types.NewBlockHeight(expiry.Uint64())

		state.Asks = append(state.Asks, &Ask{
			Price:  price,
			Expiry: ctx.BlockHeight().Add(expiryBH),
			ID:     id,
		})

		return id, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	askID, ok := out.(*big.Int)
	if !ok {
		return nil, 1, errors.NewRevertErrorf("expected an Integer return value from call, but got %T instead", out)
	}

	return askID, 0, nil
}

// GetAsks returns all the asks for this miner. (TODO: this isnt a great function signature, it returns the asks in a
// serialized array. Consider doing this some other way)
func (ma *Actor) GetAsks(ctx exec.VMContext) ([]uint64, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}
	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		var askids []uint64
		for _, ask := range state.Asks {
			if !ask.ID.IsUint64() {
				return nil, errors.NewFaultErrorf("miner ask has invalid ID (bad invariant)")
			}
			askids = append(askids, ask.ID.Uint64())
		}

		return askids, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	askids, ok := out.([]uint64)
	if !ok {
		return nil, 1, errors.NewRevertErrorf("expected a []uint64 return value from call, but got %T instead", out)
	}

	return askids, 0, nil
}

// GetAsk returns an ask by ID
func (ma *Actor) GetAsk(ctx exec.VMContext, askid *big.Int) ([]byte, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		var ask *Ask
		for _, a := range state.Asks {
			if a.ID.Cmp(askid) == 0 {
				ask = a
				break
			}
		}

		out, err := cbor.DumpObject(ask)
		if err != nil {
			return nil, err
		}

		return out, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	ask, ok := out.([]byte)
	if !ok {
		return nil, 1, errors.NewRevertErrorf("expected a Bytes return value from call, but got %T instead", out)
	}

	return ask, 0, nil
}

// GetOwner returns the miners owner.
func (ma *Actor) GetOwner(ctx exec.VMContext) (address.Address, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return address.Address{}, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.Owner, nil
	})
	if err != nil {
		return address.Address{}, errors.CodeError(err), err
	}

	a, ok := out.(address.Address)
	if !ok {
		return address.Address{}, 1, errors.NewFaultErrorf("expected an Address return value from call, but got %T instead", out)
	}

	return a, 0, nil
}

// GetLastUsedSectorID returns the last used sector id.
func (ma *Actor) GetLastUsedSectorID(ctx exec.VMContext) (uint64, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return 0, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}
	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.LastUsedSectorID, nil
	})
	if err != nil {
		return 0, errors.CodeError(err), err
	}

	a, ok := out.(uint64)
	if !ok {
		return 0, 1, errors.NewFaultErrorf("expected a uint64 sector id, but got %T instead", out)
	}

	return a, 0, nil
}

// GetSectorCommitments returns all sector commitments posted by this miner.
func (ma *Actor) GetSectorCommitments(ctx exec.VMContext) (map[string]types.Commitments, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.SectorCommitments, nil
	})
	if err != nil {
		return map[string]types.Commitments{}, errors.CodeError(err), err
	}

	a, ok := out.(map[string]types.Commitments)
	if !ok {
		return map[string]types.Commitments{}, 1, errors.NewFaultErrorf("expected a map[string]types.Commitments, but got %T instead", out)
	}

	return a, 0, nil
}

// CommitSector adds a commitment to the specified sector. The sector must not
// already be committed.
func (ma *Actor) CommitSector(ctx exec.VMContext, sectorID uint64, commD, commR, commRStar, proof []byte) (uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}
	if len(commD) != int(proofs.CommitmentBytesLen) {
		return 0, errors.NewRevertError("invalid sized commD")
	}
	if len(commR) != int(proofs.CommitmentBytesLen) {
		return 0, errors.NewRevertError("invalid sized commR")
	}
	if len(commRStar) != int(proofs.CommitmentBytesLen) {
		return 0, errors.NewRevertError("invalid sized commRStar")
	}
	// TODO: use uint64 instead of this abomination, once refmt is fixed
	// https://github.com/polydawn/refmt/issues/35
	sectorIDstr := strconv.FormatUint(sectorID, 10)

	var state State
	_, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		// verify that the caller is authorized to perform update
		if ctx.Message().From != state.Owner {
			return nil, Errors[ErrCallerUnauthorized]
		}

		_, ok := state.SectorCommitments[sectorIDstr]
		if ok {
			return nil, Errors[ErrSectorCommitted]
		}

		if state.Power.Cmp(big.NewInt(0)) == 0 {
			state.ProvingPeriodStart = ctx.BlockHeight()
		}
		inc := big.NewInt(1)
		state.Power = state.Power.Add(state.Power, inc)
		comms := types.Commitments{
			CommD:     proofs.CommD{},
			CommR:     proofs.CommR{},
			CommRStar: proofs.CommRStar{},
		}
		copy(comms.CommD[:], commD)
		copy(comms.CommR[:], commR)
		copy(comms.CommRStar[:], commRStar)
		state.LastUsedSectorID = sectorID
		state.SectorCommitments[sectorIDstr] = comms
		_, ret, err := ctx.Send(address.StorageMarketAddress, "updatePower", nil, []interface{}{inc})
		if err != nil {
			return nil, err
		}
		if ret != 0 {
			return nil, Errors[ErrStoragemarketCallFailed]
		}
		return nil, nil
	})
	if err != nil {
		return errors.CodeError(err), err
	}

	return 0, nil
}

// GetKey returns the public key for this miner.
func (ma *Actor) GetKey(ctx exec.VMContext) ([]byte, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	out, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.PublicKey, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	validOut, ok := out.([]byte)
	if !ok {
		return nil, 1, errors.NewRevertError("expected a byte slice")
	}

	return validOut, 0, nil
}

// GetPeerID returns the libp2p peer ID that this miner can be reached at.
func (ma *Actor) GetPeerID(ctx exec.VMContext) (peer.ID, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return peer.ID(""), exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State

	chunk, err := ctx.ReadStorage()
	if err != nil {
		return peer.ID(""), errors.CodeError(err), err
	}

	if err := actor.UnmarshalStorage(chunk, &state); err != nil {
		return peer.ID(""), errors.CodeError(err), err
	}

	return state.PeerID, 0, nil
}

// UpdatePeerID is used to update the peerID this miner is operating under.
func (ma *Actor) UpdatePeerID(ctx exec.VMContext, pid peer.ID) (uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var storage State
	_, err := actor.WithState(ctx, &storage, func() (interface{}, error) {
		// verify that the caller is authorized to perform update
		if ctx.Message().From != storage.Owner {
			return nil, Errors[ErrCallerUnauthorized]
		}

		storage.PeerID = pid

		return nil, nil
	})
	if err != nil {
		return errors.CodeError(err), err
	}

	return 0, nil
}

// GetPledge returns the number of pledged sectors
func (ma *Actor) GetPledge(ctx exec.VMContext) (*big.Int, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	ret, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.PledgeSectors, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	pledgeSectors, ok := ret.(*big.Int)
	if !ok {
		return nil, 1, errors.NewFaultError("Failed to retrieve pledge sectors")
	}

	return pledgeSectors, 0, nil
}

// GetPower returns the amount of proven sectors for this miner.
func (ma *Actor) GetPower(ctx exec.VMContext) (*big.Int, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	var state State
	ret, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		return state.Power, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	power, ok := ret.(*big.Int)
	if !ok {
		return nil, 1, errors.NewFaultErrorf("expected *big.Int to be returned, but got %T instead", ret)
	}

	return power, 0, nil
}

// SubmitPoSt is used to submit a coalesced PoST to the chain to convince the chain
// that you have been actually storing the files you claim to be.
func (ma *Actor) SubmitPoSt(ctx exec.VMContext, proof []byte) (uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	if len(proof) != PoStProofLength {
		return 0, errors.NewRevertError("invalid sized proof")
	}

	var state State
	_, err := actor.WithState(ctx, &state, func() (interface{}, error) {
		// verify that the caller is authorized to perform update
		if ctx.Message().From != state.Owner {
			return nil, Errors[ErrCallerUnauthorized]
		}

		// reach in to actor storage to grab comm-r for each committed sector
		var commRs []proofs.CommR
		for _, v := range state.SectorCommitments {
			commRs = append(commRs, v.CommR)
		}

		// copy message-bytes into PoStProof slice
		postProof := proofs.PoStProof{}
		copy(postProof[:], proof)

		// TODO: use IsPoStValidWithProver when proofs are implemented
		req := proofs.VerifyPoSTRequest{
			ChallengeSeed: proofs.PoStChallengeSeed{},
			CommRs:        commRs,
			Faults:        []uint64{},
			Proof:         postProof,
		}

		res, err := (&proofs.RustVerifier{}).VerifyPoST(req)
		if err != nil {
			return nil, errors.RevertErrorWrap(err, "failed to verify PoSt")
		}
		if !res.IsValid {
			return nil, Errors[ErrInvalidPoSt]
		}

		// Check if we submitted it in time
		provingPeriodEnd := state.ProvingPeriodStart.Add(ProvingPeriodBlocks)

		if ctx.BlockHeight().LessEqual(provingPeriodEnd) {
			state.ProvingPeriodStart = provingPeriodEnd
			state.LastPoSt = ctx.BlockHeight()
		} else {
			// Not great.
			// TODO: charge penalty
			return nil, errors.NewRevertErrorf("submitted PoSt late, need to pay a fee")
		}

		return nil, nil
	})
	if err != nil {
		return errors.CodeError(err), err
	}

	return 0, nil
}

// GetProvingPeriodStart returns the current ProvingPeriodStart value.
func (ma *Actor) GetProvingPeriodStart(ctx exec.VMContext) (*types.BlockHeight, uint8, error) {
	if err := ctx.Charge(100); err != nil {
		return nil, exec.ErrInsufficientGas, errors.RevertErrorWrap(err, "Insufficient gas")
	}

	chunk, err := ctx.ReadStorage()
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	var state State
	if err := actor.UnmarshalStorage(chunk, &state); err != nil {
		return nil, errors.CodeError(err), err
	}

	return state.ProvingPeriodStart, 0, nil
}
