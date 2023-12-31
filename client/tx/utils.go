package tx

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"

	"runtime/debug"

	"github.com/spf13/viper"

	"github.com/NPC-Chain/npccbase/client/account"
	"github.com/NPC-Chain/npccbase/client/context"
	"github.com/NPC-Chain/npccbase/client/keys"
	"github.com/NPC-Chain/npccbase/client/qcp"

	"github.com/NPC-Chain/npccbase/txs"
	"github.com/NPC-Chain/npccbase/types"

	"github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/crypto"

	cflags "github.com/NPC-Chain/npccbase/client/types"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

type ITxBuilder func(ctx context.CLIContext) (txs.ITx, error)

func BroadcastTxAndPrintResult(cdc *amino.Codec, txBuilder ITxBuilder) error {
	result, err := BroadcastTx(cdc, txBuilder)
	cliCtx := context.NewCLIContext().WithCodec(cdc)

	if result != nil {
		cliCtx.PrintResult(result)
	}

	return err
}

func BroadcastTx(cdc *amino.Codec, txBuilder ITxBuilder) (*ctypes.ResultBroadcastTxCommit, error) {
	isPrint := viper.GetBool(cflags.FlagPrintTx)
	cliCtx := context.NewCLIContext().WithCodec(cdc)
	signedTx, err := buildAndSignTx(cliCtx, txBuilder)
	if err != nil {
		return nil, err
	}

	v := reflect.ValueOf(signedTx)
	if v.IsNil() {
		return nil, nil
	}

	//print signed tx details
	if isPrint {
		cliCtx.PrintResult(signedTx)
	}
	return cliCtx.BroadcastTx(cdc.MustMarshalBinaryBare(signedTx))
}

func BroadcastTxs(cdc *amino.Codec, txsBuilder ITxsBuilder) (*ctypes.ResultBroadcastTxCommit, error) {
	isPrint := viper.GetBool(cflags.FlagPrintTx)
	cliCtx := context.NewCLIContext().WithCodec(cdc)
	signedTx, err := buildAndSignTxs(cliCtx, txsBuilder)
	if err != nil {
		return nil, err
	}

	v := reflect.ValueOf(signedTx)
	if v.IsNil() {
		return nil, nil
	}

	//print signed tx details
	if isPrint {
		cliCtx.PrintResult(signedTx)
	}
	return cliCtx.BroadcastTx(cdc.MustMarshalBinaryBare(signedTx))
}

func buildAndSignTx(ctx context.CLIContext, txBuilder ITxBuilder) (signedTx types.Tx, err error) {

	defer func() {
		if r := recover(); r != nil {
			log := fmt.Sprintf("buildAndSignTx error. error: %s \n recovered: %v\n", r, string(debug.Stack()))
			signedTx = nil
			err = errors.New(log)
		}
	}()

	itx, err := txBuilder(ctx)
	if err != nil {
		return nil, err
	}
	toChainID := getChainID(ctx)
	qcpMode := viper.GetBool(cflags.FlagQcp)
	if qcpMode {
		fromChainID := viper.GetString(cflags.FlagQcpFrom)
		return BuildAndSignQcpTx(ctx, itx, fromChainID, toChainID)
	} else {
		return BuildAndSignStdTx(ctx, []txs.ITx{itx}, "", toChainID)
	}
}

type ITxsBuilder func(ctx context.CLIContext) ([]txs.ITx, error)

func BroadcastTxsAndPrintResult(cdc *amino.Codec, txsBuilder ITxsBuilder) error {
	result, err := BroadcastTxs(cdc, txsBuilder)
	cliCtx := context.NewCLIContext().WithCodec(cdc)

	if result != nil {
		cliCtx.PrintResult(result)
	}

	return err
}

func buildAndSignTxs(ctx context.CLIContext, txsBuilder ITxsBuilder) (signedTx types.Tx, err error) {

	defer func() {
		if r := recover(); r != nil {
			log := fmt.Sprintf("buildAndSignTx recovered: %v\n", string(debug.Stack()))
			signedTx = nil
			err = errors.New(log)
		}
	}()

	itxs, err := txsBuilder(ctx)
	if err != nil {
		return nil, err
	}
	toChainID := getChainID(ctx)
	return BuildAndSignStdTx(ctx, itxs, "", toChainID)
}

func BuildAndSignQcpTx(ctx context.CLIContext, tx txs.ITx, fromChainID, toChainID string) (*txs.TxQcp, error) {

	qcpSigner := viper.GetString(cflags.FlagQcpSigner)
	isGenOnly := viper.GetBool(cflags.FlagGenerateOnly)

	if qcpSigner == "" || fromChainID == "" {
		return nil, errors.New("in qcp mode, --qcp-from and --qcp-signer flag must set")
	}
	qcpSignerInfo, err := keys.GetKeyInfo(ctx, qcpSigner)
	if err != nil {
		return nil, errors.New("query qcp singer info error.")
	}

	qcpSeq := getQcpInSequence(ctx, fromChainID)

	fmt.Println("> step 1. build and sign TxStd")

	txStd, err := BuildAndSignStdTx(ctx, []txs.ITx{tx}, fromChainID, toChainID)
	if err != nil {
		return nil, err
	}

	_, ok := tx.(*txs.QcpTxResult)
	txQcp := txs.NewTxQCP(txStd, fromChainID,
		toChainID,
		qcpSeq+1,
		viper.GetInt64(cflags.FlagQcpBlockHeight),
		viper.GetInt64(cflags.FlagQcpTxIndex),
		ok,
		viper.GetString(cflags.FlagQcpExtends),
	)

	if isGenOnly {
		ctx.PrintResult(txQcp)
		return nil, nil
	}

	fmt.Println("> step 2. build and sign TxQcp")
	sig, pubkey := signData(ctx, qcpSignerInfo.GetName(), txQcp.BuildSignatureBytes())
	txQcp.Sig = txs.Signature{
		Pubkey:    pubkey,
		Signature: sig,
	}

	return txQcp, nil
}

func BuildAndSignStdTx(ctx context.CLIContext, tXs []txs.ITx, fromChainID, toChainID string) (*txs.TxStd, error) {

	accountNonce := viper.GetInt64(cflags.FlagNonce)
	isGenOnly := viper.GetBool(cflags.FlagGenerateOnly)
	qcpMode := viper.GetBool(cflags.FlagQcp)

	txStd := txs.NewTxsStd(toChainID, types.NewInt(ctx.GetMaxGas()), tXs...)
	if isGenOnly && !qcpMode {
		ctx.PrintResult(txStd)
		return nil, nil
	}

	signers := getSigners(ctx, txStd.GetSigners())

	isUseFlagAccountNonce := accountNonce > 0
	for _, signerName := range signers {
		info, err := keys.GetKeyInfo(ctx, signerName)
		if err != nil {
			return nil, err
		}

		var actualNonce int64
		if isUseFlagAccountNonce {
			actualNonce = accountNonce + 1
		} else {
			nonce, err := getDefaultAccountNonce(ctx, info.GetAddress().Bytes())
			if err != nil || nonce < 0 {
				return nil, err
			}
			actualNonce = nonce + 1
		}

		txStd, err = signStdTx(ctx, signerName, actualNonce, txStd, fromChainID)
		if err != nil {
			return nil, fmt.Errorf("name %s signStdTx error: %s", signerName, err.Error())
		}
	}

	return txStd, nil
}

func signStdTx(ctx context.CLIContext, signerKeyName string, nonce int64, txStd *txs.TxStd, fromChainID string) (*txs.TxStd, error) {

	info, err := keys.GetKeyInfo(ctx, signerKeyName)
	if err != nil {
		return nil, err
	}

	addr := info.GetAddress()
	ok := false

	for _, signer := range txStd.GetSigners() {
		if bytes.Equal(addr.Bytes(), signer.Bytes()) {
			ok = true
		}
	}

	if !ok {
		return nil, fmt.Errorf("Name %s is not signer", signerKeyName)
	}

	sigdata := txStd.BuildSignatureBytes(nonce, fromChainID)
	sig, pubkey := signData(ctx, signerKeyName, sigdata)

	txStd.Signature = append(txStd.Signature, txs.Signature{
		Pubkey:    pubkey,
		Signature: sig,
		Nonce:     nonce,
	})

	return txStd, nil
}

func signData(ctx context.CLIContext, name string, data []byte) ([]byte, crypto.PubKey) {

	pass, err := keys.GetPassphrase(ctx, name)
	if err != nil {
		panic(fmt.Sprintf("Get %s Passphrase error: %s", name, err.Error()))
	}

	keybase, err := keys.GetKeyBase(ctx)
	if err != nil {
		panic(fmt.Errorf("Open keybase error.err:%s", err.Error()))
	}

	sig, pubkey, err := keybase.Sign(name, pass, data)
	if err != nil {
		panic(fmt.Errorf("Sign data error.err:%s", err.Error()))
	}

	return sig, pubkey
}

func SignDataFromAddress(ctx context.CLIContext, addr types.AccAddress, data []byte) ([]byte, crypto.PubKey) {
	keybase, err := keys.GetKeyBase(ctx)
	if err != nil {
		panic(fmt.Errorf("Open keybase error.err:%s", err.Error()))
	}

	info, err := keybase.GetByAddress(addr)
	if err != nil {
		panic(err.Error())
	}

	return signData(ctx, info.GetName(), data)
}

func getSigners(ctx context.CLIContext, txSignerAddrs []types.AccAddress) []string {

	var sortNames []string

	for _, addr := range txSignerAddrs {

		keybase, err := keys.GetKeyBase(ctx)
		if err != nil {
			panic(fmt.Errorf("Open keybase error.err:%s", err.Error()))
		}

		info, err := keybase.GetByAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("Signer addr: %s not in current keybase. err:%s", addr, err.Error()))
		}

		sortNames = append(sortNames, info.GetName())
	}

	return sortNames
}

func getQcpInSequence(ctx context.CLIContext, inChainID string) int64 {
	seq := viper.GetInt64(cflags.FlagQcpSequence)
	if seq > 0 {
		return seq
	}

	seq, _ = qcp.GetInChainSequence(ctx, inChainID)
	return seq
}

func getChainID(ctx context.CLIContext) string {
	chainID := viper.GetString(cflags.FlagChainID)
	if chainID != "" {
		return chainID
	}

	defaultChainID, err := getDefaultChainID(ctx)
	if err != nil || defaultChainID == "" {
		panic("get chain id error")
	}

	return defaultChainID
}

func getDefaultChainID(ctx context.CLIContext) (string, error) {
	client, err := ctx.GetNode()
	genesis, err := client.Genesis()
	if err != nil {
		return "", err
	}

	return genesis.Genesis.ChainID, nil
}

func getDefaultAccountNonce(ctx context.CLIContext, address []byte) (int64, error) {

	if ctx.NonceNodeURI == "" {
		return account.GetAccountNonce(ctx, address)
	}

	//NonceNodeURI不为空,使用NonceNodeURI查询account nonce值
	rpc := rpcclient.NewHTTP(ctx.NonceNodeURI, "/websocket")
	newCtx := context.NewCLIContext().WithClient(rpc).WithCodec(ctx.Codec)

	return account.GetAccountNonce(newCtx, address)
}
