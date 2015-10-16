package deployment

import (
	"github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
	"github.com/flynn/flynn/discoverd/client"
)

// deployDiscoverMeta does a one-by-one deployment but uses discoverd.Deployment
// to wait for appropriate service metadata before stopping old jobs.
func (d *DeployJob) deployDiscoverdMeta() (err error) {
	log := d.logger.New("fn", "deployDiscoverdMeta")
	log.Info("starting discoverd-meta deployment")

	defer func() {
		if err != nil {
			// TODO: support rolling back
			err = ErrSkipRollback{err.Error()}
		}
	}()

	discDeploy, err := discoverd.NewDeployment(d.serviceName)
	if err != nil {
		return err
	}

	if err := discDeploy.Reset(); err != nil {
		return err
	}
	defer discDeploy.Close()

	return d.deployOneByOneWithWaitFn(func(releaseID string, expected jobEvents, log log15.Logger) error {
		// call discoverd.Deployment.Wait if we are waiting for up
		// events, otherwise just wait for job down events.
		//
		// TODO: change the WaitFn signature to be more explicit about
		//       whether we are waiting for up or down events (the
		//       process type may well not be called "app").
		if count, ok := expected["app"]["up"]; ok && count > 0 {
			return discDeploy.Wait(count, log)
		} else {
			return d.waitForJobEvents(releaseID, expected, log)
		}
	})
}
