package dial

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

const DefaultActiveSequencerFollowerCheckDuration = 2 * DefaultDialTimeout

type ethDialer func(ctx context.Context, timeout time.Duration, log log.Logger, url string) (EthClientInterface, error)

type ActiveL2EndpointProvider struct {
	ActiveL2RollupProvider
	currentEthClient EthClientInterface
	ethDialer        ethDialer
}

func NewActiveL2EndpointProvider(
	ctx context.Context,
	ethUrls, rollupUrls []string,
	checkDuration time.Duration,
	networkTimeout time.Duration,
	logger log.Logger,
	ethDialer ethDialer,
	rollupDialer rollupDialer,
) (*ActiveL2EndpointProvider, error) {
	if len(rollupUrls) == 0 {
		return nil, errors.New("empty rollup urls list")
	}
	if len(ethUrls) != len(rollupUrls) {
		return nil, errors.New("number of eth urls and rollup urls mismatch")
	}

	rollupProvider, err := NewActiveL2RollupProvider(ctx, rollupUrls, checkDuration, networkTimeout, logger, rollupDialer)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	ethClient, err := ethDialer(cctx, networkTimeout, logger, ethUrls[0])
	if err != nil {
		return nil, fmt.Errorf("dialing eth client: %w", err)
	}
	return &ActiveL2EndpointProvider{
		ActiveL2RollupProvider: *rollupProvider,
		currentEthClient:       ethClient,
		ethDialer:              ethDialer,
	}, nil
}

func (p *ActiveL2EndpointProvider) EthClient(ctx context.Context) (EthClientInterface, error) {
	p.clientLock.Lock()
	defer p.clientLock.Unlock()
	err := p.ensureActiveEndpoint(ctx)
	if err != nil {
		return nil, err
	}

	return p.currentEthClient, nil
}

func (p *ActiveL2EndpointProvider) ensureActiveEndpoint(ctx context.Context) error {
	if !p.shouldCheck() {
		return nil
	}

	if err := p.findActiveEndpoints(ctx); err != nil {
		return err
	}
	p.activeTimeout = time.Now().Add(p.checkDuration)
	return nil
}

func (p *ActiveL2EndpointProvider) findActiveEndpoints(ctx context.Context) error {
	const maxRetries = 20
	totalAttempts := 0

	for totalAttempts < maxRetries {
		active, err := p.checkCurrentSequencer(ctx)
		if err != nil {
			p.log.Warn("Error querying active sequencer, trying next.", "err", err, "try", totalAttempts)
		} else if active {
			p.log.Debug("Current sequencer active.", "try", totalAttempts)
			return nil
		} else {
			p.log.Info("Current sequencer inactive, trying next.", "try", totalAttempts)
		}
		if err := p.dialNextSequencer(ctx); err != nil {
			return fmt.Errorf("dialing next sequencer: %w", err)
		}

		totalAttempts++
		if p.currentIndex >= p.numEndpoints() {
			p.currentIndex = 0
		}
	}
	return fmt.Errorf("failed to find an active sequencer after %d retries", maxRetries)
}

func (p *ActiveL2EndpointProvider) dialNextSequencer(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, p.networkTimeout)
	defer cancel()
	p.currentIndex++
	ep := p.rollupUrls[p.currentIndex]
	p.log.Debug("Dialing next sequencer.", "url", ep)
	rollupClient, err := p.rollupDialer(cctx, p.networkTimeout, p.log, ep)
	if err != nil {
		return fmt.Errorf("dialing rollup client: %w", err)
	}
	ethClient, err := p.ethDialer(cctx, p.networkTimeout, p.log, ep)
	if err != nil {
		return fmt.Errorf("dialing eth client: %w", err)
	}

	p.currentRollupClient = rollupClient
	p.currentEthClient = ethClient
	return nil
}

func (p *ActiveL2EndpointProvider) Close() {
	p.currentEthClient.Close()
	p.ActiveL2RollupProvider.Close()
}
