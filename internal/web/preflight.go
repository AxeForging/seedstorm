package web

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// preflightTimeout bounds how long a side may take to answer before a two-sided
// run gives up on it.
var preflightTimeout = connectTimeout

// connectBoth opens and pings the source and the target at the same time, so
// a dead side is reported within preflightTimeout and before anything is read.
// A source given as an imported snapshot is not connected (source is nil).
// Each side logs how it answered.
func (s *Server) connectBoth(ctx context.Context, log zerolog.Logger, srcRef ConnRef, snap *compare.Snapshot, tgtRef ConnRef) (source, target *Session, err error) {
	var wg sync.WaitGroup
	var srcErr, tgtErr error
	side := func(role string, ref ConnRef, out **Session, errp *error) {
		defer wg.Done()
		*errp = safego.Run("connect "+role, func() error {
			start := time.Now()
			sess, err := s.resolveConnection(ref, role)
			if err == nil {
				err = pingSession(ctx, sess)
			}
			if err != nil {
				log.Warn().Str("side", role).Err(err).Msg("Did not answer")
				return err
			}
			log.Info().Str("side", role).Str("database", sessionLabel(sess)).Dur("answered_in", time.Since(start).Round(time.Millisecond)).Msg("Connected")
			*out = sess
			return nil
		})
		*errp = runerr.OnSide(role, runerr.At(runerr.PhaseConnect, "", *errp))
	}
	if snap == nil {
		wg.Add(1)
		go side(runerr.SideSource, srcRef, &source, &srcErr)
	}
	wg.Add(1)
	go side(runerr.SideTarget, tgtRef, &target, &tgtErr)
	wg.Wait()
	if err := errors.Join(tgtErr, srcErr); err != nil {
		return nil, nil, err
	}
	return source, target, nil
}

// pingSession checks a live session still answers: a database that went away
// after it was connected must not hang the run on its first query.
func pingSession(ctx context.Context, sess *Session) error {
	pctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	return sess.Conn().PingContext(pctx)
}

// snapshotOrSessionEndpoint builds the source endpoint after connectBoth.
func snapshotOrSessionEndpoint(ctx context.Context, sess *Session, snap *compare.Snapshot) (seeder.Endpoint, error) {
	if snap != nil {
		if len(snap.Tables) == 0 {
			return seeder.Endpoint{}, errors.New("the imported counts have no tables")
		}
		label := snap.Label
		if label == "" {
			label = "imported counts"
		}
		return seeder.Endpoint{Snapshot: snap, Label: label, DBType: snap.DBType}, nil
	}
	ep, _, err := endpointFor(ctx, sess, false)
	return ep, err
}
