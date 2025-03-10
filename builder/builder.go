package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/earthly/earthly/buildcontext"
	"github.com/earthly/earthly/buildcontext/provider"
	"github.com/earthly/earthly/cleanup"
	"github.com/earthly/earthly/conslogging"
	"github.com/earthly/earthly/domain"
	"github.com/earthly/earthly/earthfile2llb"
	"github.com/earthly/earthly/states"
	"github.com/earthly/earthly/util/containerutil"
	"github.com/earthly/earthly/util/gwclientlogger"
	"github.com/earthly/earthly/util/llbutil"
	"github.com/earthly/earthly/util/llbutil/pllb"
	"github.com/earthly/earthly/variables"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/entitlements"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	reccopy "github.com/otiai10/copy"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const (
	// PhaseInit is the phase text for the init phase.
	PhaseInit = "1. Init 🚀"
	// PhaseBuild is the phase text for the build phase.
	PhaseBuild = "2. Build 🔧"
	// PhasePush is the phase text for the push phase.
	PhasePush = "3. Push ⏫"
	// PhaseOutput is the phase text for the output phase.
	PhaseOutput = "4. Local Output 🎁"
)

// Opt represent builder options.
type Opt struct {
	SessionID              string
	BkClient               *client.Client
	Console                conslogging.ConsoleLogger
	Verbose                bool
	Attachables            []session.Attachable
	Enttlmnts              []entitlements.Entitlement
	NoCache                bool
	CacheImports           *states.CacheImports
	CacheExport            string
	MaxCacheExport         string
	UseInlineCache         bool
	SaveInlineCache        bool
	ImageResolveMode       llb.ResolveMode
	CleanCollection        *cleanup.Collection
	OverridingVars         *variables.Scope
	BuildContextProvider   *provider.BuildContextProvider
	GitLookup              *buildcontext.GitLookup
	UseFakeDep             bool
	Strict                 bool
	DisableNoOutputUpdates bool
	ParallelConversion     bool
	Parallelism            *semaphore.Weighted
	LocalRegistryAddr      string
	FeatureFlagOverrides   string
	ContainerFrontend      containerutil.ContainerFrontend
}

// BuildOpt is a collection of build options.
type BuildOpt struct {
	Platform                   *specs.Platform
	AllowPrivileged            bool
	PrintPhases                bool
	Push                       bool
	NoOutput                   bool
	OnlyFinalTargetImages      bool
	OnlyArtifact               *domain.Artifact
	OnlyArtifactDestPath       string
	EnableGatewayClientLogging bool
}

// Builder executes Earthly builds.
type Builder struct {
	s         *solver
	opt       Opt
	resolver  *buildcontext.Resolver
	builtMain bool

	outDirOnce sync.Once
	outDir     string
}

// NewBuilder returns a new earthly Builder.
func NewBuilder(ctx context.Context, opt Opt) (*Builder, error) {
	b := &Builder{
		s: &solver{
			sm:              newSolverMonitor(opt.Console, opt.Verbose, opt.DisableNoOutputUpdates),
			bkClient:        opt.BkClient,
			cacheImports:    opt.CacheImports,
			cacheExport:     opt.CacheExport,
			maxCacheExport:  opt.MaxCacheExport,
			attachables:     opt.Attachables,
			enttlmnts:       opt.Enttlmnts,
			saveInlineCache: opt.SaveInlineCache,
		},
		opt:      opt,
		resolver: nil, // initialized below
	}
	b.resolver = buildcontext.NewResolver(opt.SessionID, opt.CleanCollection, opt.GitLookup, opt.Console)
	return b, nil
}

// BuildTarget executes the build of a given Earthly target.
func (b *Builder) BuildTarget(ctx context.Context, target domain.Target, opt BuildOpt) (*states.MultiTarget, error) {
	mts, err := b.convertAndBuild(ctx, target, opt)
	if err != nil {
		return nil, err
	}
	return mts, nil
}

// MakeImageAsTarBuilderFun returns a function which can be used to build an image as a tar.
func (b *Builder) MakeImageAsTarBuilderFun() states.DockerBuilderFun {
	return func(ctx context.Context, mts *states.MultiTarget, dockerTag string, outFile string) error {
		return b.buildOnlyLastImageAsTar(ctx, mts, dockerTag, outFile, BuildOpt{})
	}
}

func (b *Builder) convertAndBuild(ctx context.Context, target domain.Target, opt BuildOpt) (*states.MultiTarget, error) {
	sharedLocalStateCache := earthfile2llb.NewSharedLocalStateCache()

	featureFlagOverrides := b.opt.FeatureFlagOverrides

	destPathWhitelist := make(map[string]bool)
	manifestLists := make(map[string][]manifest) // parent image -> child images
	var mts *states.MultiTarget
	depIndex := 0
	imageIndex := 0
	dirIndex := 0
	localImages := make(map[string]string) // local reg pull name -> final name
	bf := func(childCtx context.Context, gwClient gwclient.Client) (*gwclient.Result, error) {
		if opt.EnableGatewayClientLogging {
			gwClient = gwclientlogger.New(gwClient)
		}
		var err error
		if !b.builtMain {
			mts, err = earthfile2llb.Earthfile2LLB(childCtx, target, earthfile2llb.ConvertOpt{
				GwClient:             gwClient,
				Resolver:             b.resolver,
				ImageResolveMode:     b.opt.ImageResolveMode,
				DockerBuilderFun:     b.MakeImageAsTarBuilderFun(),
				CleanCollection:      b.opt.CleanCollection,
				Platform:             opt.Platform,
				OverridingVars:       b.opt.OverridingVars,
				BuildContextProvider: b.opt.BuildContextProvider,
				CacheImports:         b.opt.CacheImports,
				UseInlineCache:       b.opt.UseInlineCache,
				UseFakeDep:           b.opt.UseFakeDep,
				AllowLocally:         !b.opt.Strict,
				AllowInteractive:     !b.opt.Strict,
				AllowPrivileged:      opt.AllowPrivileged,
				ParallelConversion:   b.opt.ParallelConversion,
				Parallelism:          b.opt.Parallelism,
				Console:              b.opt.Console,
				GitLookup:            b.opt.GitLookup,
				FeatureFlagOverrides: featureFlagOverrides,
				LocalStateCache:      sharedLocalStateCache,
			}, true)
			if err != nil {
				return nil, err
			}
		}
		res := gwclient.NewResult()
		if !b.builtMain {
			ref, err := b.stateToRef(childCtx, gwClient, mts.Final.MainState, mts.Final.Platform)
			if err != nil {
				return nil, err
			}
			res.AddRef("main", ref)
		}
		if !opt.NoOutput && opt.OnlyArtifact != nil && !opt.OnlyFinalTargetImages {
			ref, err := b.stateToRef(childCtx, gwClient, mts.Final.ArtifactsState, mts.Final.Platform)
			if err != nil {
				return nil, err
			}
			refKey := "final-artifact"
			refPrefix := fmt.Sprintf("ref/%s", refKey)
			res.AddRef(refKey, ref)
			res.AddMeta(fmt.Sprintf("%s/export-dir", refPrefix), []byte("true"))
			res.AddMeta(fmt.Sprintf("%s/final-artifact", refPrefix), []byte("true"))
		}

		isMultiPlatform := make(map[string]bool) // DockerTag -> bool
		for _, sts := range mts.All() {
			if sts.Platform != nil {
				for _, saveImage := range b.targetPhaseImages(sts) {
					if saveImage.DockerTag != "" && saveImage.DoSave {
						isMultiPlatform[saveImage.DockerTag] = true
					}
				}
			}
		}

		for _, sts := range mts.All() {
			if (sts.HasDangling && !b.opt.UseFakeDep) || (b.builtMain && sts.RunPush.HasState) {
				depRef, err := b.stateToRef(childCtx, gwClient, b.targetPhaseState(sts), sts.Platform)
				if err != nil {
					return nil, err
				}
				refKey := fmt.Sprintf("dep-%d", depIndex)
				res.AddRef(refKey, depRef)
				depIndex++
			}

			for _, saveImage := range b.targetPhaseImages(sts) {
				shouldPush := opt.Push && saveImage.Push && !sts.Target.IsRemote() && saveImage.DockerTag != "" && saveImage.DoSave
				shouldExport := !opt.NoOutput && opt.OnlyArtifact == nil && !(opt.OnlyFinalTargetImages && sts != mts.Final) && saveImage.DockerTag != "" && saveImage.DoSave
				useCacheHint := saveImage.CacheHint && b.opt.CacheExport != ""
				if (!shouldPush && !shouldExport && !useCacheHint) || (!shouldPush && saveImage.HasPushDependencies) {
					// Short-circuit.
					continue
				}
				ref, err := b.stateToRef(childCtx, gwClient, saveImage.State, sts.Platform)
				if err != nil {
					return nil, err
				}
				config, err := json.Marshal(saveImage.Image)
				if err != nil {
					return nil, errors.Wrapf(err, "marshal save image config")
				}

				if !isMultiPlatform[saveImage.DockerTag] {
					refKey := fmt.Sprintf("image-%d", imageIndex)
					refPrefix := fmt.Sprintf("ref/%s", refKey)
					imageIndex++

					localRegPullID := fmt.Sprintf("sess-%s/sp:img%d", gwClient.BuildOpts().SessionID, imageIndex)
					localImages[localRegPullID] = saveImage.DockerTag
					if shouldExport {
						if b.opt.LocalRegistryAddr != "" {
							res.AddMeta(fmt.Sprintf("%s/export-image-local-registry", refPrefix), []byte(localRegPullID))
						} else {
							res.AddMeta(fmt.Sprintf("%s/export-image", refPrefix), []byte("true"))
						}
					}
					res.AddMeta(fmt.Sprintf("%s/image.name", refPrefix), []byte(saveImage.DockerTag))
					if shouldPush {
						res.AddMeta(fmt.Sprintf("%s/export-image-push", refPrefix), []byte("true"))
						if saveImage.InsecurePush {
							res.AddMeta(fmt.Sprintf("%s/insecure-push", refPrefix), []byte("true"))
						}
					}
					res.AddMeta(fmt.Sprintf("%s/%s", refPrefix, exptypes.ExporterImageConfigKey), config)
					res.AddMeta(fmt.Sprintf("%s/image-index", refPrefix), []byte(fmt.Sprintf("%d", imageIndex)))
					res.AddRef(refKey, ref)
				} else {
					platform := llbutil.PlatformWithDefault(sts.Platform)
					platformStr := llbutil.PlatformWithDefaultToString(sts.Platform)
					// Image has platform set - need to use manifest lists.
					// Need to push as a single multi-manifest image, but output locally as
					// separate images.
					// (docker load does not support tars with manifest lists).

					// For push.
					if shouldPush {
						refKey := fmt.Sprintf("image-%d", imageIndex)
						refPrefix := fmt.Sprintf("ref/%s", refKey)
						imageIndex++

						res.AddMeta(fmt.Sprintf("%s/image.name", refPrefix), []byte(saveImage.DockerTag))
						res.AddMeta(fmt.Sprintf("%s/platform", refPrefix), []byte(platformStr))
						res.AddMeta(fmt.Sprintf("%s/export-image-push", refPrefix), []byte("true"))
						if saveImage.InsecurePush {
							res.AddMeta(fmt.Sprintf("%s/insecure-push", refPrefix), []byte("true"))
						}
						res.AddMeta(fmt.Sprintf("%s/%s", refPrefix, exptypes.ExporterImageConfigKey), config)
						res.AddMeta(fmt.Sprintf("%s/image-index", refPrefix), []byte(fmt.Sprintf("%d", imageIndex)))
						res.AddRef(refKey, ref)
					}

					// For local.
					if shouldExport {
						refKey := fmt.Sprintf("image-%d", imageIndex)
						refPrefix := fmt.Sprintf("ref/%s", refKey)
						imageIndex++

						platformImgName, err := platformSpecificImageName(saveImage.DockerTag, platform)
						if err != nil {
							return nil, err
						}
						localRegPullID, err := platformSpecificImageName(
							fmt.Sprintf("sess-%s/mp:img%d", gwClient.BuildOpts().SessionID, imageIndex), platform)
						if err != nil {
							return nil, err
						}
						localImages[localRegPullID] = platformImgName
						if b.opt.LocalRegistryAddr != "" {
							res.AddMeta(fmt.Sprintf("%s/export-image-local-registry", refPrefix), []byte(localRegPullID))
						} else {
							res.AddMeta(fmt.Sprintf("%s/export-image", refPrefix), []byte("true"))
						}
						res.AddMeta(fmt.Sprintf("%s/image.name", refPrefix), []byte(platformImgName))
						res.AddMeta(fmt.Sprintf("%s/%s", refPrefix, exptypes.ExporterImageConfigKey), config)
						res.AddMeta(fmt.Sprintf("%s/image-index", refPrefix), []byte(fmt.Sprintf("%d", imageIndex)))
						res.AddRef(refKey, ref)
						manifestLists[saveImage.DockerTag] = append(
							manifestLists[saveImage.DockerTag], manifest{
								imageName: platformImgName,
								platform:  platform,
							})
					}
				}
			}
			performSaveLocals := (!opt.NoOutput &&
				!opt.OnlyFinalTargetImages &&
				opt.OnlyArtifact == nil)

			if performSaveLocals {
				for _, saveLocal := range b.targetPhaseArtifacts(sts) {
					ref, err := b.artifactStateToRef(
						childCtx, gwClient, sts.SeparateArtifactsState[saveLocal.Index],
						sts.Platform)
					if err != nil {
						return nil, err
					}
					refKey := fmt.Sprintf("dir-%d", dirIndex)
					refPrefix := fmt.Sprintf("ref/%s", refKey)
					res.AddRef(refKey, ref)
					artifact := domain.Artifact{
						Target:   sts.Target,
						Artifact: saveLocal.ArtifactPath,
					}
					res.AddMeta(fmt.Sprintf("%s/artifact", refPrefix), []byte(artifact.String()))
					res.AddMeta(fmt.Sprintf("%s/src-path", refPrefix), []byte(saveLocal.ArtifactPath))
					res.AddMeta(fmt.Sprintf("%s/dest-path", refPrefix), []byte(saveLocal.DestPath))
					res.AddMeta(fmt.Sprintf("%s/export-dir", refPrefix), []byte("true"))
					res.AddMeta(fmt.Sprintf("%s/dir-index", refPrefix), []byte(fmt.Sprintf("%d", dirIndex)))
					destPathWhitelist[saveLocal.DestPath] = true
					dirIndex++
				}
			}

			targetInteractiveSession := b.targetPhaseInteractiveSession(sts)
			if targetInteractiveSession.Initialized && targetInteractiveSession.Kind == states.SessionEphemeral {
				ref, err := b.stateToRef(ctx, gwClient, targetInteractiveSession.State, sts.Platform)
				res.AddRef("ephemeral", ref)
				if err != nil {
					return nil, err
				}
			}
		}
		return res, nil
	}
	onImage := func(childCtx context.Context, eg *errgroup.Group, imageName string) (io.WriteCloser, error) {
		pipeR, pipeW := io.Pipe()
		eg.Go(func() error {
			defer pipeR.Close()
			err := loadDockerTar(childCtx, b.opt.ContainerFrontend, pipeR, b.opt.Console)
			if err != nil {
				return errors.Wrapf(err, "load docker tar")
			}
			return nil
		})
		return pipeW, nil
	}
	onArtifact := func(childCtx context.Context, index int, artifact domain.Artifact, artifactPath string, destPath string) (string, error) {
		if !destPathWhitelist[destPath] {
			return "", errors.Errorf("dest path %s is not in the whitelist: %+v", destPath, destPathWhitelist)
		}
		outDir, err := b.tempEarthlyOutDir()
		if err != nil {
			return "", err
		}
		artifactDir := filepath.Join(outDir, fmt.Sprintf("index-%d", index))
		err = os.MkdirAll(artifactDir, 0755)
		if err != nil {
			return "", errors.Wrapf(err, "create dir %s", artifactDir)
		}
		return artifactDir, nil
	}
	onFinalArtifact := func(childCtx context.Context) (string, error) {
		return b.tempEarthlyOutDir()
	}
	onPull := func(childCtx context.Context, imagesToPull []string) error {
		if b.opt.LocalRegistryAddr == "" {
			return nil
		}
		pullMap := make(map[string]string)
		for _, imgToPull := range imagesToPull {
			finalName, ok := localImages[imgToPull]
			if !ok {
				return errors.Errorf("unrecognized image to pull %s", imgToPull)
			}
			pullMap[imgToPull] = finalName
		}
		return dockerPullLocalImages(childCtx, b.opt.ContainerFrontend, b.opt.LocalRegistryAddr, pullMap, b.opt.Console)
	}
	if opt.PrintPhases {
		b.opt.Console.PrintPhaseHeader(PhaseBuild, false, "")
	}
	err := b.s.buildMainMulti(ctx, bf, onImage, onArtifact, onFinalArtifact, onPull, PhaseBuild)
	if err != nil {
		return nil, errors.Wrapf(err, "build main")
	}
	if opt.PrintPhases {
		b.opt.Console.PrintPhaseFooter(PhaseBuild, false, "")
	}
	b.builtMain = true

	if opt.PrintPhases {
		b.opt.Console.PrintPhaseHeader(PhasePush, !opt.Push, "")
		if !opt.Push {
			b.opt.Console.Printf("To enable pushing use\n\n\t\tearthly --push ...\n\n")
		}
	}
	if opt.Push && opt.OnlyArtifact == nil && !opt.OnlyFinalTargetImages {
		hasRunPush := false
		for _, sts := range mts.All() {
			if sts.RunPush.HasState {
				hasRunPush = true
				break
			}
		}
		if hasRunPush {
			err = b.s.buildMainMulti(ctx, bf, onImage, onArtifact, onFinalArtifact, onPull, PhasePush)
			if err != nil {
				return nil, errors.Wrapf(err, "build push")
			}
		}
	}

	pushConsole := conslogging.NewBufferedLogger(&b.opt.Console)
	outputConsole := conslogging.NewBufferedLogger(&b.opt.Console)
	outputPhaseSpecial := ""

	if opt.NoOutput {
		// Nothing.
	} else if opt.OnlyArtifact != nil {
		outputPhaseSpecial = "single artifact"
		outDir, err := b.tempEarthlyOutDir()
		if err != nil {
			return nil, err
		}
		err = b.saveArtifactLocally(
			ctx, outputConsole, *opt.OnlyArtifact, outDir, opt.OnlyArtifactDestPath,
			mts.Final.ID, opt, false)
		if err != nil {
			return nil, err
		}
	} else if opt.OnlyFinalTargetImages {
		outputPhaseSpecial = "single image"
		for _, saveImage := range mts.Final.SaveImages {
			shouldPush := opt.Push && saveImage.Push && saveImage.DockerTag != "" && saveImage.DoSave
			shouldExport := !opt.NoOutput && saveImage.DockerTag != "" && saveImage.DoSave
			if !shouldPush && !shouldExport {
				continue
			}
			targetStr := b.opt.Console.PrefixColor().Sprintf("%s", mts.Final.Target.StringCanonical())
			if shouldPush {
				pushConsole.Printf("Pushed image %s as %s\n", targetStr, saveImage.DockerTag)
			}
			if saveImage.Push && !opt.Push {
				pushConsole.Printf("Did not push image %s\n", saveImage.DockerTag)
			}
			outputConsole.Printf("Image %s output as %s\n", targetStr, saveImage.DockerTag)
		}
	} else {
		// This needs to match with the same index used during output.
		// TODO: This is a little brittle to future code changes.
		dirIndex := 0
		for _, sts := range mts.All() {
			console := b.opt.Console.WithPrefixAndSalt(sts.Target.String(), sts.ID)
			for _, saveImage := range sts.SaveImages {
				shouldPush := opt.Push && saveImage.Push && !sts.Target.IsRemote() && saveImage.DockerTag != "" && saveImage.DoSave
				shouldExport := !opt.NoOutput && saveImage.DockerTag != "" && saveImage.DoSave
				if !shouldPush && !shouldExport {
					continue
				}
				targetStr := console.PrefixColor().Sprintf("%s", sts.Target.StringCanonical())
				if shouldPush {
					pushConsole.Printf("Pushed image %s as %s\n", targetStr, saveImage.DockerTag)
				}
				if saveImage.Push && !opt.Push && !sts.Target.IsRemote() {
					pushConsole.Printf("Did not push image %s\n", saveImage.DockerTag)
				}
				outputConsole.Printf("Image %s output as %s\n", targetStr, saveImage.DockerTag)
			}
			for _, saveLocal := range sts.SaveLocals {
				outDir, err := b.tempEarthlyOutDir()
				if err != nil {
					return nil, err
				}
				artifactDir := filepath.Join(outDir, fmt.Sprintf("index-%d", dirIndex))
				artifact := domain.Artifact{
					Target:   sts.Target,
					Artifact: saveLocal.ArtifactPath,
				}
				err = b.saveArtifactLocally(
					ctx, outputConsole, artifact, artifactDir, saveLocal.DestPath,
					sts.ID, opt, saveLocal.IfExists)
				if err != nil {
					return nil, err
				}
				dirIndex++
			}

			if sts.RunPush.HasState {
				if opt.Push {
					for _, saveLocal := range sts.RunPush.SaveLocals {
						outDir, err := b.tempEarthlyOutDir()
						if err != nil {
							return nil, err
						}
						artifactDir := filepath.Join(outDir, fmt.Sprintf("index-%d", dirIndex))
						artifact := domain.Artifact{
							Target:   sts.Target,
							Artifact: saveLocal.ArtifactPath,
						}
						err = b.saveArtifactLocally(
							ctx, outputConsole, artifact, artifactDir, saveLocal.DestPath,
							sts.ID, opt, saveLocal.IfExists)
						if err != nil {
							return nil, err
						}
						dirIndex++
					}
				} else {
					for _, commandStr := range sts.RunPush.CommandStrs {
						pushConsole.Printf("Did not execute push command %s\n", commandStr)
					}

					for _, saveImage := range sts.RunPush.SaveImages {
						pushConsole.Printf(
							"Did not push image %s as evaluating the image would "+
								"have caused a RUN --push to execute", saveImage.DockerTag)
						outputConsole.Printf("Did not output image %s locally, "+
							"as evaluating the image would have caused a "+
							"RUN --push to execute", saveImage.DockerTag)
					}

					if sts.RunPush.InteractiveSession.Initialized {
						pushConsole.Printf("Did not start an %s interactive session "+
							"with command %s\n", sts.RunPush.InteractiveSession.Kind,
							sts.RunPush.InteractiveSession.CommandStr)
					}
				}
			}
		}
	}
	pushConsole.Flush()
	if opt.PrintPhases {
		b.opt.Console.PrintPhaseFooter(PhasePush, !opt.Push, "")
		b.opt.Console.PrintPhaseHeader(PhaseOutput, opt.NoOutput, outputPhaseSpecial)
	}
	outputConsole.Flush()
	for parentImageName, children := range manifestLists {
		err = loadDockerManifest(ctx, b.opt.Console, b.opt.ContainerFrontend, parentImageName, children)
		if err != nil {
			return nil, err
		}
	}
	if opt.PrintPhases {
		b.opt.Console.PrintPhaseFooter(PhaseOutput, false, "")
		b.opt.Console.PrintSuccess()
	}
	return mts, nil
}

func (b *Builder) targetPhaseState(sts *states.SingleTarget) pllb.State {
	if b.builtMain {
		return sts.RunPush.State
	}
	return sts.MainState
}

func (b *Builder) targetPhaseArtifacts(sts *states.SingleTarget) []states.SaveLocal {
	if b.builtMain {
		return sts.RunPush.SaveLocals
	}
	return sts.SaveLocals
}

func (b *Builder) targetPhaseImages(sts *states.SingleTarget) []states.SaveImage {
	if b.builtMain {
		return sts.RunPush.SaveImages
	}
	return sts.SaveImages
}

func (b *Builder) targetPhaseInteractiveSession(sts *states.SingleTarget) states.InteractiveSession {
	if b.builtMain {
		return sts.RunPush.InteractiveSession
	}
	return sts.InteractiveSession
}

func (b *Builder) stateToRef(ctx context.Context, gwClient gwclient.Client, state pllb.State, platform *specs.Platform) (gwclient.Reference, error) {
	if b.opt.NoCache && !b.builtMain {
		state = state.SetMarshalDefaults(llb.IgnoreCache)
	}
	return llbutil.StateToRef(ctx, gwClient, state, platform, b.opt.CacheImports.AsMap())
}

func (b *Builder) artifactStateToRef(ctx context.Context, gwClient gwclient.Client, state pllb.State, platform *specs.Platform) (gwclient.Reference, error) {
	if b.opt.NoCache || b.builtMain {
		state = state.SetMarshalDefaults(llb.IgnoreCache)
	}
	return llbutil.StateToRef(ctx, gwClient, state, platform, b.opt.CacheImports.AsMap())
}

func (b *Builder) buildOnlyLastImageAsTar(ctx context.Context, mts *states.MultiTarget, dockerTag string, outFile string, opt BuildOpt) error {
	saveImage := mts.Final.LastSaveImage()
	err := b.buildMain(ctx, mts, opt)
	if err != nil {
		return err
	}

	platform, err := llbutil.ResolvePlatform(mts.Final.Platform, opt.Platform)
	if err != nil {
		platform = mts.Final.Platform
	}
	plat := llbutil.PlatformWithDefault(platform)
	err = b.outputImageTar(ctx, saveImage, plat, dockerTag, outFile)
	if err != nil {
		return err
	}
	return nil
}

func (b *Builder) buildMain(ctx context.Context, mts *states.MultiTarget, opt BuildOpt) error {
	state := mts.Final.MainState
	if b.opt.NoCache {
		state = state.SetMarshalDefaults(llb.IgnoreCache)
	}
	platform, err := llbutil.ResolvePlatform(mts.Final.Platform, opt.Platform)
	if err != nil {
		platform = mts.Final.Platform
	}
	plat := llbutil.PlatformWithDefault(platform)
	err = b.s.solveMain(ctx, state, plat)
	if err != nil {
		return errors.Wrapf(err, "solve side effects")
	}
	return nil
}

func (b *Builder) outputImageTar(ctx context.Context, saveImage states.SaveImage, platform specs.Platform, dockerTag string, outFile string) error {
	err := b.s.solveDockerTar(ctx, saveImage.State, platform, saveImage.Image, dockerTag, outFile)
	if err != nil {
		return errors.Wrapf(err, "solve image tar %s", outFile)
	}
	return nil
}

func (b *Builder) saveArtifactLocally(ctx context.Context, console *conslogging.BufferedLogger, artifact domain.Artifact, indexOutDir string, destPath string, salt string, opt BuildOpt, ifExists bool) error {
	fromPattern := filepath.Join(indexOutDir, filepath.FromSlash(artifact.Artifact))
	// Resolve possible wildcards.
	// TODO: Note that this is not very portable, as the glob is host-platform dependent,
	//       while the pattern is also guest-platform dependent.
	fromGlobMatches, err := filepath.Glob(fromPattern)
	if err != nil {
		return errors.Wrapf(err, "glob")
	} else if !artifact.Target.IsRemote() && len(fromGlobMatches) <= 0 {
		if ifExists {
			return nil
		}
		return errors.Errorf("cannot save artifact %s, since it does not exist", artifact.StringCanonical())
	}
	isWildcard := strings.ContainsAny(fromPattern, `*?[`)
	for _, from := range fromGlobMatches {
		fiSrc, err := os.Stat(from)
		if err != nil {
			return errors.Wrapf(err, "os stat %s", from)
		}
		srcIsDir := fiSrc.IsDir()
		to := destPath
		destIsDir := strings.HasSuffix(to, "/")
		if artifact.Target.IsLocalExternal() && !filepath.IsAbs(to) {
			// Place within external dir.
			to = path.Join(artifact.Target.LocalPath, to)
		}
		if destIsDir {
			// Place within dest dir.
			to = path.Join(to, path.Base(from))
		}
		destExists := false
		fiDest, err := os.Stat(to)
		if err != nil {
			// Ignore err. Likely dest path does not exist.
			if isWildcard && !destIsDir {
				return errors.New(
					"artifact is a wildcard, but AS LOCAL destination does not end with /")
			}
			destIsDir = fiSrc.IsDir()
		} else {
			destExists = true
			destIsDir = fiDest.IsDir()
		}
		if srcIsDir && !destIsDir {
			return errors.New(
				"artifact is a directory, but existing AS LOCAL destination is a file")
		}
		if destExists {
			if !srcIsDir {
				// Remove pre-existing dest file.
				err = os.Remove(to)
				if err != nil {
					return errors.Wrapf(err, "rm %s", to)
				}
			} else {
				// Remove pre-existing dest dir.
				err = os.RemoveAll(to)
				if err != nil {
					return errors.Wrapf(err, "rm -rf %s", to)
				}
			}
		}

		toDir := path.Dir(to)
		err = os.MkdirAll(toDir, 0755)
		if err != nil {
			return errors.Wrapf(err, "mkdir all for artifact %s", toDir)
		}
		err = os.Link(from, to)
		if err != nil {
			// Hard linking did not work. Try recursive copy.
			errCopy := reccopy.Copy(from, to)
			if errCopy != nil {
				return errors.Wrapf(errCopy, "copy artifact %s", from)
			}
		}

		// Write to console about this artifact.
		artifactPath := trimFilePathPrefix(indexOutDir, from, b.opt.Console)
		artifact2 := domain.Artifact{
			Target:   artifact.Target,
			Artifact: artifactPath,
		}
		destPath2 := filepath.FromSlash(destPath)
		if strings.HasSuffix(destPath, "/") {
			destPath2 = filepath.Join(destPath2, filepath.Base(artifactPath))
		}
		if opt.PrintPhases {
			artifactColor := b.opt.Console.WithPrefixAndSalt(artifact.Target.String(), salt).PrefixColor()
			artifactStr := artifactColor.Sprintf("%s", artifact2.StringCanonical())
			console.Printf("Artifact %s output as %s\n", artifactStr, destPath2)
		}
	}
	return nil
}

func (b *Builder) tempEarthlyOutDir() (string, error) {
	var err error
	b.outDirOnce.Do(func() {
		tmpParentDir := ".tmp-earthly-out"
		err = os.MkdirAll(tmpParentDir, 0755)
		if err != nil {
			err = errors.Wrapf(err, "unable to create dir %s", tmpParentDir)
			return
		}
		b.outDir, err = ioutil.TempDir(tmpParentDir, "tmp")
		if err != nil {
			err = errors.Wrap(err, "mk temp dir for artifacts")
			return
		}
		b.opt.CleanCollection.Add(func() error {
			remErr := os.RemoveAll(b.outDir)
			// Remove the parent dir only if it's empty.
			_ = os.Remove(tmpParentDir)
			return remErr
		})
	})
	return b.outDir, err
}

func trimFilePathPrefix(prefix string, thePath string, console conslogging.ConsoleLogger) string {
	ret, err := filepath.Rel(prefix, thePath)
	if err != nil {
		console.Warnf("Warning: Could not compute relative path for %s "+
			"as being relative to %s: %s\n", thePath, prefix, err.Error())
		return thePath
	}
	return ret
}
