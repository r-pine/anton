package indexer

import (
	"context"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/xssnick/tonutils-go/tlb"

	"github.com/iam047801/tonidx/internal/core"
	"github.com/iam047801/tonidx/internal/core/repository/abi"
)

func (s *Service) getSourceTxHash(ctx context.Context, in *core.Message, outMsgMap map[uint64]*core.Message) ([]byte, error) {
	if !in.Incoming || in.Type != core.Internal {
		return nil, errors.Wrap(core.ErrNotAvailable, "msg is not incoming or internal")
	}

	out, ok := outMsgMap[in.CreatedLT]
	if ok {
		return out.TxHash, nil
	}

	sourceTx, err := s.txRepo.GetSourceMessageTxHash(ctx, in.SrcAddress, in.DstAddress, in.CreatedLT) // TODO: batch request (?)
	if err != nil {
		return nil, err
	}

	return sourceTx, nil
}

func (s *Service) processBlockMessages(ctx context.Context, _ *tlb.BlockInfo, blockTx []*tlb.Transaction) ([]*core.Message, error) {
	var (
		inMessages  []*core.Message
		outMessages []*core.Message
		outMsgMap   = make(map[uint64]*core.Message)
	)

	for _, tx := range blockTx {
		for _, outMsg := range tx.IO.Out {
			msg, err := mapMessage(false, tx, outMsg)
			if err != nil {
				return nil, errors.Wrap(err, "map outcoming message")
			}
			if err = abi.ParseOperationID(msg); err != nil {
				return nil, errors.Wrapf(err, "parse operation (tx_hash = %x, msg_hash = %x)", tx.Hash, msg.BodyHash)
			}
			outMessages = append(outMessages, msg)
			outMsgMap[msg.CreatedLT] = msg
		}
	}

	for _, tx := range blockTx {
		if tx.IO.In == nil {
			continue
		}

		msg, err := mapMessage(true, tx, tx.IO.In)
		if err != nil {
			return nil, errors.Wrap(err, "map incoming message")
		}

		msg.SourceTxHash, err = s.getSourceTxHash(ctx, msg, outMsgMap)
		if err != nil && !errors.Is(err, core.ErrNotAvailable) {
			if !errors.Is(err, core.ErrNotFound) {
				return nil, errors.Wrapf(err, "get source msg hash (tx_hash = %x)", tx.Hash)
			}
			log.Error().Err(err).Hex("tx_hash", tx.Hash).Uint64("created_lt", msg.CreatedLT).Msg("cannot get source msg hash")
		}

		if err = abi.ParseOperationID(msg); err != nil {
			return nil, errors.Wrapf(err, "parse operation (tx_hash = %x, msg_hash = %x)", tx.Hash, msg.BodyHash)
		}

		inMessages = append(inMessages, msg)
	}

	return append(outMessages, inMessages...), nil
}

func (s *Service) parseMessagePayloads(ctx context.Context, messages []*core.Message, accountMap map[string]*core.AccountState) (ret []*core.MessagePayload) {
	for _, msg := range messages {
		if msg.Type != core.Internal {
			continue // TODO: external message parsing
		}

		src, ok := accountMap[msg.SrcAddress]
		if !ok {
			log.Debug().Str("src_addr", msg.SrcAddress).Msg("cannot find src account")
			continue
		}
		dst, ok := accountMap[msg.DstAddress]
		if !ok {
			log.Debug().Str("src_addr", msg.SrcAddress).Msg("cannot find src account")
			continue
		}

		payload, err := s.parser.ParseMessagePayload(ctx, src, dst, msg)
		if errors.Is(err, core.ErrNotAvailable) {
			continue
		}
		if err != nil {
			log.Error().Err(err).Hex("msg_hash", msg.BodyHash).Hex("tx_hash", msg.TxHash).Msg("parse message payload")
			continue
		}
		ret = append(ret, payload)
	}

	return ret
}
