// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package dockertest_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/ory/dockertest/v3"
	dc "github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	docker = os.Getenv("DOCKER_URL")
	pool   *dockertest.Pool
)

func TestMain(m *testing.M) {
	var err error
	pool, err = dockertest.NewPool(docker)
	if err != nil {
		log.Fatalf("Could not construct pool: %s", err)
	}
	err = pool.Client.Ping()
	if err != nil {
		log.Fatalf("Could not connect to Docker: %s", err)
	}
	os.Exit(m.Run())
}

func TestPostgres(t *testing.T) {
	resource, err := pool.Run("postgres", "9.5", []string{"POSTGRES_PASSWORD=secret"})
	require.NoError(t, err)
	assert.NotEmpty(t, resource.GetPort("5432/tcp"))

	assert.NotEmpty(t, resource.GetBoundIP("5432/tcp"))

	err = pool.Retry(func() error {
		db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:secret@localhost:%s/postgres?sslmode=disable", resource.GetPort("5432/tcp")))
		if err != nil {
			return err
		}
		return db.Ping()
	})
	require.NoError(t, err)
	require.NoError(t, pool.Purge(resource))
}

func TestMongo(t *testing.T) {
	options := &dockertest.RunOptions{
		Repository: "mongo",
		Tag:        "3.3.12",
		Cmd:        []string{"mongod", "--smallfiles", "--port", "3000"},
		// expose a different port
		ExposedPorts: []string{"3000"},
	}
	resource, err := pool.RunWithOptions(options)
	require.NoError(t, err)
	port := resource.GetPort("3000/tcp")
	assert.NotEmpty(t, port)

	err = pool.Retry(func() error {
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s", port))
		if err != nil {
			return err
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("could not connect to resource")
		}

		return nil
	})
	require.NoError(t, err)
	require.NoError(t, pool.Purge(resource))
}

func TestMysqlWithPlatform(t *testing.T) {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "mysql",
		Tag:        "8.0",
		Env:        []string{"MYSQL_ROOT_PASSWORD=secret"},
		Platform:   "", // Platform in the format os[/arch[/variant]] (e.g. linux/amd64). Default: ""
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resource.GetPort("3306/tcp"))

	err = pool.Retry(func() error {
		var err error
		db, err := sql.Open("mysql", fmt.Sprintf("root:secret@(localhost:%s)/mysql", resource.GetPort("3306/tcp")))
		if err != nil {
			return err
		}
		return db.Ping()
	})
	require.NoError(t, err)

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithName(t *testing.T) {
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
		})
	require.NoError(t, err)
	assert.Equal(t, "/db", resource.Container.Name)

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithLabels(t *testing.T) {
	labels := map[string]string{
		"my": "label",
	}
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
			Labels:     labels,
			Env:        []string{"POSTGRES_PASSWORD=secret"},
		})
	require.NoError(t, err)
	assert.EqualValues(t, labels, resource.Container.Config.Labels, "labels don't match")

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithUser(t *testing.T) {
	user := "1001:1001"
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
			User:       user,
			Env:        []string{"POSTGRES_PASSWORD=secret"},
		})
	require.NoError(t, err)
	assert.EqualValues(t, user, resource.Container.Config.User, "users don't match")

	res, err := pool.Client.InspectContainer(resource.Container.ID)
	require.NoError(t, err)
	assert.Equal(t, user, res.Config.User)

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithTty(t *testing.T) {
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
			Env:        []string{"POSTGRES_PASSWORD=secret"},
			Tty:        true,
		})
	require.NoError(t, err)
	assert.True(t, resource.Container.Config.Tty, "tty is false")

	res, err := pool.Client.InspectContainer(resource.Container.ID)
	require.NoError(t, err)
	assert.True(t, res.Config.Tty)

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithPortBinding(t *testing.T) {
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "postgres",
			Tag:        "9.5",
			PortBindings: map[dc.Port][]dc.PortBinding{
				"5432/tcp": {{HostIP: "", HostPort: "5433"}},
			},
			Env: []string{"POSTGRES_PASSWORD=secret"},
		})
	require.NoError(t, err)
	assert.Equal(t, "5433", resource.GetPort("5432/tcp"))

	require.NoError(t, pool.Purge(resource))
}

func TestBuildImage(t *testing.T) {
	// Create Dockerfile in temp dir
	dir := t.TempDir()

	dockerfilePath := dir + "/Dockerfile"
	require.NoError(t, os.WriteFile(dockerfilePath,
		[]byte("FROM postgres:9.5"),
		0o644,
	))

	resource, err := pool.BuildAndRun("postgres-test", dockerfilePath, nil)
	require.NoError(t, err)

	assert.Equal(t, "/postgres-test", resource.Container.Name)
	require.NoError(t, pool.Purge(resource))
}

func TestBuildImageWithBuildArg(t *testing.T) {
	// Create Dockerfile in temp dir
	dir := t.TempDir()

	dockerfilePath := dir + "/Dockerfile"
	require.NoError(t, os.WriteFile(dockerfilePath,
		[]byte((`FROM busybox
ARG foo
RUN echo -n $foo > /build-time-value
CMD sleep 10
`)),
		0o644,
	))

	resource, err := pool.BuildAndRunWithBuildOptions(
		&dockertest.BuildOptions{
			ContextDir: dir,
			Dockerfile: "Dockerfile",
			BuildArgs: []dc.BuildArg{
				{Name: "foo", Value: "bar"},
			},
		},
		&dockertest.RunOptions{
			Name: "buildarg-test",
		}, func(hc *dc.HostConfig) {
			hc.AutoRemove = true
		})
	require.NoError(t, err)

	var stdout bytes.Buffer
	exitCode, err := resource.Exec(
		[]string{"cat", "/build-time-value"},
		dockertest.ExecOptions{StdOut: &stdout},
	)
	require.NoError(t, err)
	require.Zero(t, exitCode)
	require.Equal(t, "bar", stdout.String())
	require.NoError(t, pool.Purge(resource))
}

func TestExpire(t *testing.T) {
	resource, err := pool.Run("postgres", "9.5", []string{"POSTGRES_PASSWORD=secret"})
	require.NoError(t, err)
	assert.NotEmpty(t, resource.GetPort("5432/tcp"))

	assert.NotEmpty(t, resource.GetBoundIP("5432/tcp"))

	err = pool.Retry(func() error {
		db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:secret@localhost:%s/postgres?sslmode=disable", resource.GetPort("5432/tcp")))
		if err != nil {
			return err
		}
		err = db.Ping()
		if err != nil {
			return nil
		}
		err = resource.Expire(1)
		require.NoError(t, err)
		time.Sleep(5 * time.Second)
		err = db.Ping()
		require.Error(t, err)
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, pool.Purge(resource))
}

func TestContainerWithShMzSize(t *testing.T) {
	shmemsize := int64(1024 * 1024)
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
		}, func(hostConfig *dc.HostConfig) {
			hostConfig.ShmSize = shmemsize
		})
	require.NoError(t, err)
	assert.EqualValues(t, shmemsize, resource.Container.HostConfig.ShmSize, "shmsize don't match")

	require.NoError(t, pool.Purge(resource))
}

func TestContainerByName(t *testing.T) {
	got, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
			Env:        []string{"POSTGRES_PASSWORD=secret"},
		})
	require.NoError(t, err)

	want, ok := pool.ContainerByName("db")
	require.True(t, ok)

	require.Equal(t, want, got)

	require.NoError(t, pool.Purge(got))
}

func TestRemoveContainerByName(t *testing.T) {
	_, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
			Env:        []string{"POSTGRES_PASSWORD=secret"},
		})
	require.NoError(t, err)

	err = pool.RemoveContainerByName("db")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "db",
			Repository: "postgres",
			Tag:        "9.5",
		})
	require.NoError(t, err)
	require.NoError(t, pool.Purge(resource))
}

func TestExec(t *testing.T) {
	resource, err := pool.Run("postgres", "9.5", []string{"POSTGRES_PASSWORD=secret"})
	require.NoError(t, err)
	assert.NotEmpty(t, resource.GetPort("5432/tcp"))
	assert.NotEmpty(t, resource.GetBoundIP("5432/tcp"))

	defer resource.Close()

	var pgVersion string
	err = pool.Retry(func() error {
		db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:secret@localhost:%s/postgres?sslmode=disable", resource.GetPort("5432/tcp")))
		if err != nil {
			return err
		}
		return db.QueryRow("SHOW server_version").Scan(&pgVersion)
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	exitCode, err := resource.Exec(
		[]string{"psql", "-qtAX", "-U", "postgres", "-c", "SHOW server_version"},
		dockertest.ExecOptions{StdOut: &stdout},
	)
	require.NoError(t, err)
	require.Zero(t, exitCode)

	require.Equal(t, pgVersion, strings.TrimRight(stdout.String(), "\n"))
}

func TestNetworking_on_start(t *testing.T) {
	network, err := pool.CreateNetwork("test-on-start")
	require.NoError(t, err)
	defer network.Close()

	resourceFirst, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "9.5",
		Networks:   []*dockertest.Network{network},
		Env:        []string{"POSTGRES_PASSWORD=secret"},
	})
	require.NoError(t, err)
	defer resourceFirst.Close()

	resourceSecond, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "11",
		Networks:   []*dockertest.Network{network},
		Env:        []string{"POSTGRES_PASSWORD=secret"},
	})
	require.NoError(t, err)
	defer resourceSecond.Close()

	var expectedVersion string
	err = pool.Retry(func() error {
		db, err := sql.Open(
			"postgres",
			fmt.Sprintf(
				"postgres://postgres:secret@localhost:%s/postgres?sslmode=disable",
				resourceSecond.GetPort("5432/tcp"),
			),
		)
		if err != nil {
			return err
		}
		return db.QueryRow("SHOW server_version").Scan(&expectedVersion)
	})
	require.NoError(t, err)
}

func TestNetworking_after_start(t *testing.T) {
	network, err := pool.CreateNetwork("test-after-start")
	require.NoError(t, err)
	defer network.Close()

	resourceFirst, err := pool.Run("postgres", "9.6", []string{"POSTGRES_PASSWORD=secret"})
	require.NoError(t, err)
	defer resourceFirst.Close()

	err = resourceFirst.ConnectToNetwork(network)
	require.NoError(t, err)

	resourceSecond, err := pool.Run("postgres", "11", []string{"POSTGRES_PASSWORD=secret"})
	require.NoError(t, err)
	defer resourceSecond.Close()

	err = resourceSecond.ConnectToNetwork(network)
	require.NoError(t, err)

	var expectedVersion string
	err = pool.Retry(func() error {
		db, err := sql.Open(
			"postgres",
			fmt.Sprintf(
				"postgres://postgres:secret@localhost:%s/postgres?sslmode=disable",
				resourceSecond.GetPort("5432/tcp"),
			),
		)
		if err != nil {
			return err
		}
		return db.QueryRow("SHOW server_version").Scan(&expectedVersion)
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	exitCode, err := resourceFirst.Exec(
		[]string{"psql", "-qtAX", "-h", resourceSecond.GetIPInNetwork(network), "-U", "postgres", "-c", "SHOW server_version"},
		dockertest.ExecOptions{StdOut: &stdout, Env: []string{"PGPASSWORD=secret"}},
	)
	require.NoError(t, err)
	require.Zero(t, exitCode)

	require.Equal(t, expectedVersion, strings.TrimRight(stdout.String(), "\n"))
}

func TestClientRaceCondition(t *testing.T) {
	// Shadow pool so that we can have a fresh client with nil pool.Client.serverAPIVersion
	pool, err := dockertest.NewPool(docker)
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			// Tests must be run in parallel to recreate the issue
			t.Parallel()
			resource, _ := pool.RunWithOptions(
				&dockertest.RunOptions{
					Repository: "postgres",
					Tag:        "13.4",
				},
			)
			defer pool.Purge(resource)
		})
	}
}

func TestExecStatus(t *testing.T) {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "alpine",
		Tag:        "3.16",
		Cmd:        []string{"tail", "-f", "/dev/null"},
	})
	require.NoError(t, err)
	defer resource.Close()
	exitCode, err := resource.Exec([]string{"/bin/false"}, dockertest.ExecOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, exitCode)
	exitCode, err = resource.Exec([]string{"/bin/sh", "-c", "/bin/sleep 2 && exit 42"}, dockertest.ExecOptions{})
	require.NoError(t, err)
	require.Equal(t, 42, exitCode)
}

func TestOutboundIPPortBinding(t *testing.T) {
	outboundIP := func() string {
		conn, err := net.Dial("udp", "8.8.8.8:80")
		require.NoError(t, err)
		defer conn.Close()
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		return localAddr.IP.String()
	}()
	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "postgres",
			Tag:        "9.5",
			Env:        []string{"POSTGRES_PASSWORD=secret"},
			PortBindings: map[dc.Port][]dc.PortBinding{
				"5432/tcp": {{HostIP: outboundIP, HostPort: "0"}},
			},
		})
	require.NoError(t, err)
	mappedPort := resource.GetPort("5432/tcp")
	require.NotEmpty(t, mappedPort)
	boundIP := resource.GetBoundIP("5432/tcp")
	require.Equal(t, outboundIP, boundIP)
	require.NoError(t, pool.Purge(resource))
}
