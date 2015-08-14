// Copyright 2015 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bs

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/fsouza/go-dockerclient"
	"github.com/tsuru/config"
	"github.com/tsuru/docker-cluster/cluster"
	"github.com/tsuru/tsuru/app"
	"github.com/tsuru/tsuru/db"
	"github.com/tsuru/tsuru/db/storage"
	"github.com/tsuru/tsuru/log"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

var digestRegexp = regexp.MustCompile(`(?m)^Digest: (.*)$`)

type DockerProvisioner interface {
	Cluster() *cluster.Cluster
	RegistryAuthConfig() docker.AuthConfiguration
}

const (
	// QueueTaskName is the name of the task that starts bs container on a
	// given node.
	QueueTaskName = "run-bs"

	bsUniqueID = "bs"
)

type Env struct {
	Name  string
	Value string
}

type PoolEnvs struct {
	Name string
	Envs []Env
}

type Config struct {
	ID    string `bson:"_id"`
	Image string
	Token string
	Envs  []Env
	Pools []PoolEnvs
}

type EnvMap map[string]string

type PoolEnvMap map[string]EnvMap

func (conf *Config) UpdateEnvMaps(envMap EnvMap, poolEnvMap PoolEnvMap) error {
	forbiddenList := map[string]bool{
		"DOCKER_ENDPOINT":       true,
		"TSURU_ENDPOINT":        true,
		"SYSLOG_LISTEN_ADDRESS": true,
		"TSURU_TOKEN":           true,
	}
	for _, env := range conf.Envs {
		if forbiddenList[env.Name] {
			return fmt.Errorf("cannot set %s variable", env.Name)
		}
		if env.Value == "" {
			delete(envMap, env.Name)
		} else {
			envMap[env.Name] = env.Value
		}
	}
	for _, p := range conf.Pools {
		if poolEnvMap[p.Name] == nil {
			poolEnvMap[p.Name] = make(EnvMap)
		}
		for _, env := range p.Envs {
			if forbiddenList[env.Name] {
				return fmt.Errorf("cannot set %s variable", env.Name)
			}
			if env.Value == "" {
				delete(poolEnvMap[p.Name], env.Name)
			} else {
				poolEnvMap[p.Name][env.Name] = env.Value
			}
		}
	}
	return nil
}

func (conf *Config) getImage() string {
	if conf != nil && conf.Image != "" {
		return conf.Image
	}
	bsImage, _ := config.GetString("docker:bs:image")
	if bsImage == "" {
		bsImage = "tsuru/bs"
	}
	return bsImage
}

func (conf *Config) EnvListForEndpoint(dockerEndpoint, poolName string) ([]string, error) {
	tsuruEndpoint, _ := config.GetString("host")
	if !strings.HasPrefix(tsuruEndpoint, "http://") && !strings.HasPrefix(tsuruEndpoint, "https://") {
		tsuruEndpoint = "http://" + tsuruEndpoint
	}
	tsuruEndpoint = strings.TrimRight(tsuruEndpoint, "/") + "/"
	endpoint := dockerEndpoint
	socket, _ := config.GetString("docker:bs:socket")
	if socket != "" {
		endpoint = "unix:///var/run/docker.sock"
	}
	token, err := conf.getToken()
	if err != nil {
		return nil, err
	}
	envList := []string{
		"DOCKER_ENDPOINT=" + endpoint,
		"TSURU_ENDPOINT=" + tsuruEndpoint,
		"TSURU_TOKEN=" + token,
		"SYSLOG_LISTEN_ADDRESS=udp://0.0.0.0:" + strconv.Itoa(SysLogPort()),
	}
	envMap := EnvMap{}
	poolEnvMap := PoolEnvMap{}
	err = conf.UpdateEnvMaps(envMap, poolEnvMap)
	if err != nil {
		return nil, err
	}
	for envName, envValue := range envMap {
		envList = append(envList, fmt.Sprintf("%s=%s", envName, envValue))
	}
	for envName, envValue := range poolEnvMap[poolName] {
		envList = append(envList, fmt.Sprintf("%s=%s", envName, envValue))
	}
	return envList, nil
}

func (conf *Config) getToken() (string, error) {
	if conf.Token != "" {
		return conf.Token, nil
	}
	coll, err := collection()
	if err != nil {
		return "", err
	}
	defer coll.Close()
	tokenData, err := app.AuthScheme.AppLogin(app.InternalAppName)
	if err != nil {
		return "", err
	}
	token := tokenData.GetValue()
	_, err = coll.Upsert(bson.M{
		"_id": bsUniqueID,
		"$or": []bson.M{{"token": ""}, {"token": bson.M{"$exists": false}}},
	}, bson.M{"$set": bson.M{"token": token}})
	if err == nil {
		conf.Token = token
		return token, nil
	}
	app.AuthScheme.Logout(token)
	if !mgo.IsDup(err) {
		return "", err
	}
	err = coll.FindId(bsUniqueID).One(conf)
	if err != nil {
		return "", err
	}
	return conf.Token, nil
}

func bsConfigFromEnvMaps(envMap EnvMap, poolEnvMap PoolEnvMap) *Config {
	var finalConf Config
	for name, value := range envMap {
		finalConf.Envs = append(finalConf.Envs, Env{Name: name, Value: value})
	}
	for poolName, envMap := range poolEnvMap {
		poolEnv := PoolEnvs{Name: poolName}
		for name, value := range envMap {
			poolEnv.Envs = append(poolEnv.Envs, Env{Name: name, Value: value})
		}
		finalConf.Pools = append(finalConf.Pools, poolEnv)
	}
	return &finalConf
}

func SysLogPort() int {
	bsPort, _ := config.GetInt("docker:bs:syslog-port")
	if bsPort == 0 {
		bsPort = 1514
	}
	return bsPort
}

func SaveImage(digest string) error {
	coll, err := collection()
	if err != nil {
		return err
	}
	defer coll.Close()
	_, err = coll.UpsertId(bsUniqueID, bson.M{"$set": bson.M{"image": digest}})
	return err
}

func SaveEnvs(envMap EnvMap, poolEnvMap PoolEnvMap) error {
	finalConf := bsConfigFromEnvMaps(envMap, poolEnvMap)
	coll, err := collection()
	if err != nil {
		return err
	}
	defer coll.Close()
	_, err = coll.UpsertId(bsUniqueID, bson.M{"$set": bson.M{"envs": finalConf.Envs, "pools": finalConf.Pools}})
	return err
}

func LoadConfig() (*Config, error) {
	var config Config
	coll, err := collection()
	if err != nil {
		return nil, err
	}
	defer coll.Close()
	err = coll.FindId(bsUniqueID).One(&config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func collection() (*storage.Collection, error) {
	conn, err := db.Conn()
	if err != nil {
		return nil, err
	}
	return conn.Collection("bsconfig"), nil
}

// CreateContainer creates the bs container on the given node.
//
// The relaunch flag defines the behavior when there's already a bs container
// running in the target host: when relaunch is true, the function will remove
// the running container and launch another. Otherwise, it will just return an
// error indicating that the container is already running.
func CreateContainer(dockerEndpoint, poolName string, p DockerProvisioner, relaunch bool) error {
	client, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		return err
	}
	bsConf, err := LoadConfig()
	if err != nil {
		if err != mgo.ErrNotFound {
			return err
		}
		bsConf = &Config{}
	}
	bsImage := bsConf.getImage()
	err = pullBsImage(bsImage, dockerEndpoint, p)
	if err != nil {
		return err
	}
	hostConfig := docker.HostConfig{
		RestartPolicy: docker.AlwaysRestart(),
		Privileged:    true,
		NetworkMode:   "host",
	}
	socket, _ := config.GetString("docker:bs:socket")
	if socket != "" {
		hostConfig.Binds = []string{fmt.Sprintf("%s:/var/run/docker.sock:rw", socket)}
	}
	env, err := bsConf.EnvListForEndpoint(dockerEndpoint, poolName)
	if err != nil {
		return err
	}
	opts := docker.CreateContainerOptions{
		Name:       "big-sibling",
		HostConfig: &hostConfig,
		Config: &docker.Config{
			Image: bsImage,
			Env:   env,
		},
	}
	container, err := client.CreateContainer(opts)
	if relaunch && err == docker.ErrContainerAlreadyExists {
		err = client.RemoveContainer(docker.RemoveContainerOptions{ID: opts.Name, Force: true})
		if err != nil {
			return err
		}
		container, err = client.CreateContainer(opts)
	}
	if err != nil {
		return err
	}
	return client.StartContainer(container.ID, &hostConfig)
}

func pullBsImage(image, dockerEndpoint string, p DockerProvisioner) error {
	client, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	pullOpts := docker.PullImageOptions{Repository: image, OutputStream: &buf}
	err = client.PullImage(pullOpts, p.RegistryAuthConfig())
	if err != nil {
		return err
	}
	if shouldPinBsImage(image) {
		match := digestRegexp.FindAllStringSubmatch(buf.String(), 1)
		if len(match) > 0 {
			image += "@" + match[0][1]
		}
	}
	return SaveImage(image)
}

func shouldPinBsImage(image string) bool {
	parts := strings.SplitN(image, "/", 3)
	lastPart := parts[len(parts)-1]
	return len(strings.SplitN(lastPart, ":", 2)) < 2
}

// RecreateContainers relaunch all bs containers in the cluster for the given
// DockerProvisioner.
func RecreateContainers(p DockerProvisioner) error {
	cluster := p.Cluster()
	nodes, err := cluster.UnfilteredNodes()
	if err != nil {
		return err
	}
	errChan := make(chan error, len(nodes))
	wg := sync.WaitGroup{}
	log.Debugf("[bs containers] recreating %d containers", len(nodes))
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := &nodes[i]
			pool := node.Metadata["pool"]
			log.Debugf("[bs containers] recreating container in %s [%s]", node.Address, pool)
			err := CreateContainer(node.Address, pool, p, true)
			if err != nil {
				msg := fmt.Sprintf("[bs containers] failed to create container in %s [%s]: %s", node.Address, pool, err)
				log.Error(msg)
				err = errors.New(msg)
				errChan <- err
			}
		}(i)
	}
	wg.Wait()
	close(errChan)
	return <-errChan
}
