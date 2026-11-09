// Package lead lets one replica of a worker act at a time: the one holding
// the worker's Lease. The others stand by and take over within a lease's
// duration once the holder stops renewing it, so a worker can run more than
// one replica without two of them carrying out the same request.
package lead

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Config names the Lease and the replica competing for it.
type Config struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Identity is the replica's name, its pod's.
	Identity string `json:"-"`
	// LeaseDuration, RenewDeadline and RetryPeriod default to 15, 10 and 2
	// seconds. A Lease keeps its duration in whole seconds.
	LeaseDuration, RenewDeadline, RetryPeriod time.Duration `json:"-"`
}

// ErrLost reports that a replica stopped holding the Lease while it worked.
// The process exits, so nothing it started outlives its turn; started
// again, it stands by.
var ErrLost = errors.New("lost the lease")

// Run runs work while this replica holds the Lease, and returns when ctx
// ends, with nil, or when the Lease is lost, with ErrLost. Standing by,
// it waits.
func Run(ctx context.Context, client kubernetes.Interface, c Config, log *slog.Logger, work func(context.Context) error) error {
	if c.LeaseDuration == 0 {
		c.LeaseDuration, c.RenewDeadline, c.RetryPeriod = 15*time.Second, 10*time.Second, 2*time.Second
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Namespace: c.Namespace, Name: c.Name},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: c.Identity},
	}
	worked := make(chan error, 1)
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: lock, Name: c.Name, ReleaseOnCancel: true,
		LeaseDuration: c.LeaseDuration, RenewDeadline: c.RenewDeadline, RetryPeriod: c.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				log.Info("holding the lease; working", "lease", c.Namespace+"/"+c.Name, "identity", c.Identity)
				worked <- work(ctx)
			},
			OnStoppedLeading: func() {},
			OnNewLeader: func(holder string) {
				if holder != c.Identity {
					log.Info("standing by", "lease", c.Namespace+"/"+c.Name, "holder", holder)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("lease %s/%s: %w", c.Namespace, c.Name, err)
	}
	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		elector.Run(inner)
		close(stopped)
	}()
	select {
	case err := <-worked:
		cancel()
		<-stopped
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			return err
		}
		return ErrLost
	case <-stopped:
		if ctx.Err() != nil {
			return nil
		}
		// Run gave up on the Lease while work had not returned yet.
		if err := <-worked; err != nil {
			return err
		}
		return ErrLost
	}
}
