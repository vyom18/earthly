package earthfile2llb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	//"sort"
	//"strings"

	//"github.com/containerd/containerd/platforms"
	"github.com/earthly/earthly/dockertar"
	"github.com/earthly/earthly/domain"
	"github.com/earthly/earthly/llbutil"
	"github.com/earthly/earthly/states"
	//"github.com/earthly/earthly/states/dedup"
	"github.com/moby/buildkit/client/llb"
	//gwclient "github.com/moby/buildkit/frontend/gateway/client"
	//specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	//"gopkg.in/yaml.v3"
)

type withLocalDockerRun struct {
	c        *Converter
	tarLoads []llb.State
}

func (wdr *withLocalDockerRun) Run(ctx context.Context, args []string, opt WithDockerOpt) error {
	for _, loadOpt := range opt.Loads {
		// Load.
		err := wdr.load(ctx, loadOpt)
		if err != nil {
			return errors.Wrap(err, "load")
		}
	}

	for index, tarContext := range wdr.tarLoads {
		fmt.Printf("TODO %v %v\n", index, tarContext)
	}

	//var tarPaths []string
	//for index, tarContext := range wdr.tarLoads {
	//	loadDir := fmt.Sprintf("/var/earthly/load-%d", index)
	//	runOpts = append(runOpts, llb.AddMount(loadDir, tarContext, llb.Readonly))
	//	tarPaths = append(tarPaths, path.Join(loadDir, "image.tar"))
	//}

	// TODO extend the gwclient to push tar images to it
	// it might look something like this:
	//	composeConfigDt, err := ref.ReadFile(ctx, gwclient.ReadRequest{
	//		Filename: fmt.Sprintf("/tmp/earthly/%s", composeConfigFile),
	//	})

	return wdr.c.RunLocal(ctx, []string{"echo", "TODO", fmt.Sprintf("%v", args)}, false)

	//finalArgs := args
	//if opt.WithEntrypoint {
	//	if len(args) == 0 {
	//		// No args provided. Use the image's CMD.
	//		args := make([]string, len(wdr.c.mts.Final.MainImage.Config.Cmd))
	//		copy(args, wdr.c.mts.Final.MainImage.Config.Cmd)
	//	}
	//	finalArgs = append(wdr.c.mts.Final.MainImage.Config.Entrypoint, args...)
	//	opt.WithShell = false // Don't use shell when --entrypoint is passed.
	//}
	//runOpts = append(runOpts, llb.Security(llb.SecurityModeInsecure))
	//runStr := fmt.Sprintf(
	//	"WITH DOCKER RUN %s%s",
	//	strIf(opt.WithEntrypoint, "--entrypoint "),
	//	strings.Join(finalArgs, " "))
	//runOpts = append(runOpts, llb.WithCustomNamef("%s%s", wdr.c.vertexPrefix(false), runStr))
	//dindID, err := wdr.c.mts.Final.TargetInput.Hash()
	//if err != nil {
	//	return errors.Wrap(err, "compute dind id")
	//}
	//shellWrap := makeWithDockerdWrapFun(dindID, tarPaths, opt)
	//_, err = wdr.c.internalRun(
	//	ctx, finalArgs, opt.Secrets, opt.WithShell, shellWrap,
	//	false, false, false, opt.NoCache, runStr, runOpts...)
	//return err
}

//func (wdr *withLocalDockerRun) getComposePulls(ctx context.Context, opt WithDockerOpt) ([]DockerPullOpt, error) {
//	if len(opt.ComposeFiles) == 0 {
//		// Quick way out. Compose not used.
//		return nil, nil
//	}
//	// Get compose images from compose config.
//	composeConfigDt, err := wdr.getComposeConfig(ctx, opt)
//	if err != nil {
//		return nil, err
//	}
//	type composeService struct {
//		Image    string `yaml:"image"`
//		Platform string `yaml:"platform"`
//	}
//	type composeData struct {
//		Services map[string]composeService `yaml:"services"`
//	}
//	var config composeData
//	err = yaml.Unmarshal(composeConfigDt, &config)
//	if err != nil {
//		return nil, errors.Wrapf(err, "parse compose config for %v", opt.ComposeFiles)
//	}
//
//	// Collect relevant images from the comopose config.
//	composeServicesSet := make(map[string]bool)
//	for _, composeService := range opt.ComposeServices {
//		composeServicesSet[composeService] = true
//	}
//	var pulls []DockerPullOpt
//	for serviceName, serviceInfo := range config.Services {
//		if serviceInfo.Image == "" {
//			// Image not specified in yaml.
//			continue
//		}
//		platform := wdr.c.opt.Platform
//		if serviceInfo.Platform != "" {
//			p, err := platforms.Parse(serviceInfo.Platform)
//			if err != nil {
//				return nil, errors.Wrapf(
//					err, "parse platform for image %s: %s", serviceInfo.Image, serviceInfo.Platform)
//			}
//			platform = &p
//		}
//		if len(opt.ComposeServices) > 0 {
//			if composeServicesSet[serviceName] {
//				pulls = append(pulls, DockerPullOpt{
//					ImageName: serviceInfo.Image,
//					Platform:  platform,
//				})
//			}
//		} else {
//			// No services specified. Special case: collect all.
//			pulls = append(pulls, DockerPullOpt{
//				ImageName: serviceInfo.Image,
//				Platform:  platform,
//			})
//		}
//	}
//	return pulls, nil
//}
//
//func (wdr *withLocalDockerRun) pull(ctx context.Context, opt DockerPullOpt) error {
//	plat := llbutil.PlatformWithDefault(opt.Platform)
//	state, image, _, err := wdr.c.internalFromClassical(
//		ctx, opt.ImageName, plat,
//		llb.WithCustomNamef("%sDOCKER PULL %s", wdr.c.imageVertexPrefix(opt.ImageName), opt.ImageName),
//	)
//	if err != nil {
//		return err
//	}
//	mts := &states.MultiTarget{
//		Final: &states.SingleTarget{
//			MainState: state,
//			MainImage: image,
//			TargetInput: dedup.TargetInput{
//				TargetCanonical: fmt.Sprintf("+@docker-pull:%s", opt.ImageName),
//			},
//			SaveImages: []states.SaveImage{
//				{
//					State:     state,
//					Image:     image,
//					DockerTag: opt.ImageName,
//				},
//			},
//		},
//	}
//	return wdr.solveImage(
//		ctx, mts, opt.ImageName, opt.ImageName,
//		llb.WithCustomNamef("%sDOCKER LOAD (PULL %s)", wdr.c.imageVertexPrefix(opt.ImageName), opt.ImageName))
//}

func (wdr *withLocalDockerRun) load(ctx context.Context, opt DockerLoadOpt) error {
	depTarget, err := domain.ParseTarget(opt.Target)
	if err != nil {
		return errors.Wrapf(err, "parse target %s", opt.Target)
	}
	mts, err := wdr.c.buildTarget(ctx, depTarget.String(), opt.Platform, opt.BuildArgs, false)
	if err != nil {
		return err
	}
	if opt.ImageName == "" {
		// Infer image name from the SAVE IMAGE statement.
		if len(mts.Final.SaveImages) == 0 || mts.Final.SaveImages[0].DockerTag == "" {
			return errors.New(
				"no docker image tag specified in load and it cannot be inferred from the SAVE IMAGE statement")
		}
		if len(mts.Final.SaveImages) > 1 {
			return errors.New(
				"no docker image tag specified in load and it cannot be inferred from the SAVE IMAGE statement: " +
					"multiple tags mentioned in SAVE IMAGE")
		}
		opt.ImageName = mts.Final.SaveImages[0].DockerTag
	}
	return wdr.solveImage(
		ctx, mts, depTarget.String(), opt.ImageName,
		llb.WithCustomNamef(
			"%sDOCKER LOAD %s %s", wdr.c.imageVertexPrefix(depTarget.String()), depTarget.String(), opt.ImageName))
}

func (wdr *withLocalDockerRun) solveImage(ctx context.Context, mts *states.MultiTarget, opName string, dockerTag string, opts ...llb.RunOption) error {
	solveID, err := states.KeyFromHashAndTag(mts.Final, dockerTag)
	if err != nil {
		return errors.Wrap(err, "state key func")
	}
	tarContext, found := wdr.c.opt.SolveCache.Get(solveID)
	if found {
		wdr.tarLoads = append(wdr.tarLoads, tarContext)
		return nil
	}
	// Use a builder to create docker .tar file, mount it via a local build context,
	// then docker load it within the current side effects state.
	outDir, err := ioutil.TempDir("/tmp", "earthly-docker-load")
	if err != nil {
		return errors.Wrap(err, "mk temp dir for docker load")
	}
	wdr.c.opt.CleanCollection.Add(func() error {
		return os.RemoveAll(outDir)
	})
	outFile := path.Join(outDir, "image.tar")
	err = wdr.c.opt.DockerBuilderFun(ctx, mts, dockerTag, outFile)
	if err != nil {
		return errors.Wrapf(err, "build target %s for docker load", opName)
	}
	dockerImageID, err := dockertar.GetID(outFile)
	if err != nil {
		return errors.Wrap(err, "inspect docker tar after build")
	}
	// Use the docker image ID + dockerTag as sessionID. This will cause
	// buildkit to use cache when these are the same as before (eg a docker image
	// that is identical as before).
	sessionIDKey := fmt.Sprintf("%s-%s", dockerTag, dockerImageID)
	sha256SessionIDKey := sha256.Sum256([]byte(sessionIDKey))
	sessionID := hex.EncodeToString(sha256SessionIDKey[:])
	// Add the tar to the local context.
	tarContext = llb.Local(
		string(solveID),
		llb.SessionID(sessionID),
		llb.Platform(llbutil.DefaultPlatform()),
		llb.WithCustomNamef("[internal] docker tar context %s %s", opName, sessionID),
	)
	fmt.Printf("adding to tarloads %v\n", tarContext)
	wdr.tarLoads = append(wdr.tarLoads, tarContext)
	wdr.c.mts.Final.LocalDirs[string(solveID)] = outDir
	wdr.c.opt.SolveCache.Set(solveID, tarContext)
	return nil
}

//func (wdr *withLocalDockerRun) getComposeConfig(ctx context.Context, opt WithDockerOpt) ([]byte, error) {
//	panic("not supported")
//	// Add the right run to fetch the docker compose config.
//	params := composeParams(opt)
//	args := []string{
//		"/bin/sh", "-c",
//		fmt.Sprintf(
//			"%s %s get-compose-config",
//			strings.Join(params, " "),
//			dockerdWrapperPath),
//	}
//	runOpts := []llb.RunOption{
//		llb.AddMount(
//			dockerdWrapperPath, llb.Scratch(), llb.HostBind(), llb.SourcePath(dockerdWrapperPath)),
//		llb.Args(args),
//		llb.WithCustomNamef("%sWITH DOCKER (docker-compose config)", wdr.c.vertexPrefix(false)),
//	}
//	state := wdr.c.mts.Final.MainState.Run(runOpts...).Root()
//	ref, err := llbutil.StateToRef(ctx, wdr.c.opt.GwClient, state, wdr.c.opt.Platform, wdr.c.opt.CacheImports)
//	if err != nil {
//		return nil, errors.Wrap(err, "state to ref compose config")
//	}
//	composeConfigDt, err := ref.ReadFile(ctx, gwclient.ReadRequest{
//		Filename: fmt.Sprintf("/tmp/earthly/%s", composeConfigFile),
//	})
//	if err != nil {
//		return nil, errors.Wrap(err, "read compose config file")
//	}
//	return composeConfigDt, nil
//}
//
//func composeParams(opt WithDockerOpt) []string {
//	panic("not supported")
//	return []string{
//		fmt.Sprintf("EARTHLY_START_COMPOSE=\"%t\"", (len(opt.ComposeFiles) > 0)),
//		fmt.Sprintf("EARTHLY_COMPOSE_FILES=\"%s\"", strings.Join(opt.ComposeFiles, " ")),
//		fmt.Sprintf("EARTHLY_COMPOSE_SERVICES=\"%s\"", strings.Join(opt.ComposeServices, " ")),
//		// fmt.Sprintf("EARTHLY_DEBUG=\"true\""),
//	}
//}
