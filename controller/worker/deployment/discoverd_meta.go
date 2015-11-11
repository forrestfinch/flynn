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

	discDeploys := make(map[string]*discoverd.Deployment)

	for typ, serviceName := range d.serviceNames {
		discDeploy, err := discoverd.NewDeployment(serviceName)
		if err != nil {
			return err
		}
		discDeploys[typ] = discDeploy
		if err := discDeploy.Reset(); err != nil {
			return err
		}
		defer discDeploy.Close()
	}

	return d.deployOneByOneWithWaitFn(func(releaseID string, expected jobEvents, log log15.Logger) error {
		// TODO(jpg): Properly handle more than 1 process
		for typ, events := range expected {
			if count, ok := events["up"]; ok && count > 0 {
				if discDeploy, ok := discDeploys[typ]; ok {
					return discDeploy.Wait(count, log)
				}
			}
		}
		return d.waitForJobEvents(releaseID, expected, log)
	})
}
