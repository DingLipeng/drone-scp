package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appleboy/com/random"
	"github.com/appleboy/easyssh-proxy"
	"github.com/fatih/color"
)

var (
	errMissingHost           = errors.New("error: missing server host")
	errMissingPasswordOrKey  = errors.New("error: can't connect without a private SSH key or password")
	errSetPasswordAndKey     = errors.New("can't set password and key at the same time")
	errMissingSourceOrTarget = errors.New("missing source or target config")
	errorNoSource            = errors.New("can't find source files or directory")
)

type (
	// Repo information.
	Repo struct {
		Owner string
		Name  string
	}

	// Build information.
	Build struct {
		Event   string
		Number  int
		Commit  string
		Message string
		Branch  string
		Author  string
		Status  string
		Link    string
	}

	// Config for the plugin.
	Config struct {
		Host              []string
		Port              string
		Username          string
		Password          string
		Key               string
		Passphrase        string
		Fingerprint       string
		KeyPath           string
		Timeout           time.Duration
		CommandTimeout    time.Duration
		Target            []string
		Source            []string
		Remove            bool
		StripComponents   int
		TarExec           string
		TarTmpPath        string
		Proxy             easyssh.DefaultConfig
		Debug             bool
		Overwrite         bool
		UnlinkFirst       bool
		Ciphers           []string
		UseInsecureCipher bool
	}

	// Plugin values.
	Plugin struct {
		Repo     Repo
		Build    Build
		Config   Config
		DestFile string
	}

	copyError struct {
		host    string
		message string
	}
)

func (e copyError) Error() string {
	return fmt.Sprintf("error copy file to dest: %s, error message: %s\n", e.host, e.message)
}

func trimPath(keys []string) []string {
	var newKeys []string

	for _, value := range keys {
		value = strings.Trim(value, " ")
		if len(value) == 0 {
			continue
		}

		newKeys = append(newKeys, value)
	}

	return newKeys
}

func globList(paths []string) fileList {
	var list fileList

	for _, pattern := range paths {
		pattern = strings.Trim(pattern, " ")
		if string(pattern[0]) == "?" {
			pattern = pattern[1:]
			list.Ignore = append(list.Ignore, pattern)
		} else {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				fmt.Printf("Glob error for %q: %s\n", pattern, err)
				continue
			}
			list.Source = append(list.Source, matches...)
		}
	}

	return list
}

func buildArgs(tar string, files fileList) []string {
	var args []string
	if len(files.Ignore) > 0 {
		for _, v := range files.Ignore {
			args = append(args, "--exclude")
			args = append(args, v)
		}
	}
	args = append(args, "-zvcf")
	args = append(args, getRealPath(tar))
	args = append(args, files.Source...)

	return args
}

func (p Plugin) log(host string, message ...interface{}) {
	if count := len(p.Config.Host); count == 1 {
		fmt.Printf("%s", fmt.Sprintln(message...))
	} else {
		fmt.Printf("%s: %s", host, fmt.Sprintln(message...))
	}
}

func (p *Plugin) removeDestFile(ssh *easyssh.MakeConfig) error {
	p.log(ssh.Server, "remove file", p.DestFile)
	_, errStr, _, err := ssh.Run(rmcmd(p.DestFile), p.Config.CommandTimeout)
	if err != nil {
		return err
	}

	if errStr != "" {
		return errors.New(errStr)
	}

	return nil
}

func (p *Plugin) removeAllDestFile() error {
	for _, host := range p.Config.Host {
		ssh := &easyssh.MakeConfig{
			Server:            host,
			User:              p.Config.Username,
			Password:          p.Config.Password,
			Port:              p.Config.Port,
			Key:               p.Config.Key,
			KeyPath:           p.Config.KeyPath,
			Passphrase:        p.Config.Passphrase,
			Timeout:           p.Config.Timeout,
			Ciphers:           p.Config.Ciphers,
			Fingerprint:       p.Config.Fingerprint,
			UseInsecureCipher: p.Config.UseInsecureCipher,
			Proxy: easyssh.DefaultConfig{
				Server:            p.Config.Proxy.Server,
				User:              p.Config.Proxy.User,
				Password:          p.Config.Proxy.Password,
				Port:              p.Config.Proxy.Port,
				Key:               p.Config.Proxy.Key,
				KeyPath:           p.Config.Proxy.KeyPath,
				Passphrase:        p.Config.Proxy.Passphrase,
				Timeout:           p.Config.Proxy.Timeout,
				Ciphers:           p.Config.Proxy.Ciphers,
				Fingerprint:       p.Config.Proxy.Fingerprint,
				UseInsecureCipher: p.Config.Proxy.UseInsecureCipher,
			},
		}

		// remove tar file
		err := p.removeDestFile(ssh)
		if err != nil {
			return err
		}
	}

	return nil
}

type fileList struct {
	Ignore []string
	Source []string
}

func (p *Plugin) buildArgs(target string) []string {
	var args []string

	var options string
	if p.Config.Debug {
		options = "-zvxf"
	} else {
		options = "-zxf"
	}

	args = append(args,
		p.Config.TarExec,
		options,
		p.DestFile,
	)

	if p.Config.StripComponents > 0 {
		args = append(args, "--strip-components")
		args = append(args, strconv.Itoa(p.Config.StripComponents))
	}

	if p.Config.Overwrite {
		args = append(args, "--overwrite")
	}

	if p.Config.UnlinkFirst {
		args = append(args, "--unlink-first")
	}

	args = append(args,
		"-C",
		target,
	)

	return args
}

// Exec executes the plugin.
func (p *Plugin) Exec() error {
	if len(p.Config.Host) == 0 {
		return errMissingHost
	}

	if len(p.Config.Key) == 0 && len(p.Config.Password) == 0 && len(p.Config.KeyPath) == 0 {
		return errMissingPasswordOrKey
	}

	if len(p.Config.Key) != 0 && len(p.Config.Password) != 0 {
		return errSetPasswordAndKey
	}

	if len(p.Config.Source) == 0 || len(p.Config.Target) == 0 {
		return errMissingSourceOrTarget
	}

	if p.Config.Debug {
		fmt.Println("the source is:")
		fmt.Println(p.Config.Source)
	}

	files := globList(trimPath(p.Config.Source))
	if len(files.Source) == 0 {
		return errorNoSource
	}

	p.DestFile = fmt.Sprintf("%s.tar.gz", random.String(10))

	// create a temporary file for the archive
	dir := os.TempDir()
	tar := filepath.Join(dir, p.DestFile)

	// run archive command
	fmt.Println("tar all files into " + tar)
	if p.Config.Debug {
		checkCmd := exec.Command("sh", "-c", fmt.Sprintf("%s --version|grep %s", p.Config.TarExec, p.Config.TarExec))
		output, err := checkCmd.CombinedOutput()
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		fmt.Println(string(output))
	}

	args := buildArgs(tar, files)
	cmd := exec.Command(p.Config.TarExec, args...)
	if p.Config.Debug {
		fmt.Println("$", strings.Join(cmd.Args, " "))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	wg := sync.WaitGroup{}
	wg.Add(len(p.Config.Host))
	errChannel := make(chan error)
	finished := make(chan struct{})
	for _, host := range p.Config.Host {
		go func(host string) {
			// Create MakeConfig instance with remote username, server address and path to private key.
			ssh := &easyssh.MakeConfig{
				Server:            host,
				User:              p.Config.Username,
				Password:          p.Config.Password,
				Port:              p.Config.Port,
				Key:               p.Config.Key,
				KeyPath:           p.Config.KeyPath,
				Passphrase:        p.Config.Passphrase,
				Timeout:           p.Config.Timeout,
				Ciphers:           p.Config.Ciphers,
				Fingerprint:       p.Config.Fingerprint,
				UseInsecureCipher: p.Config.UseInsecureCipher,
				Proxy: easyssh.DefaultConfig{
					Server:            p.Config.Proxy.Server,
					User:              p.Config.Proxy.User,
					Password:          p.Config.Proxy.Password,
					Port:              p.Config.Proxy.Port,
					Key:               p.Config.Proxy.Key,
					KeyPath:           p.Config.Proxy.KeyPath,
					Passphrase:        p.Config.Proxy.Passphrase,
					Timeout:           p.Config.Proxy.Timeout,
					Ciphers:           p.Config.Proxy.Ciphers,
					Fingerprint:       p.Config.Proxy.Fingerprint,
					UseInsecureCipher: p.Config.Proxy.UseInsecureCipher,
				},
			}

			// upload file to the tmp path
			p.DestFile = fmt.Sprintf("%s%s", p.Config.TarTmpPath, p.DestFile)

			// Call Scp method with file you want to upload to remote server.
			p.log(host, "scp file to server.")
			err := ssh.Scp(tar, p.DestFile)
			if err != nil {
				errChannel <- copyError{host, err.Error()}
				return
			}

			for _, target := range p.Config.Target {
				// remove target folder before upload data
				if p.Config.Remove {
					p.log(host, "Remove target folder:", target)

					_, _, _, err := ssh.Run(rmcmd(target), p.Config.CommandTimeout)
					if err != nil {
						errChannel <- err
						return
					}
				}

				p.log(host, "create folder", target)
				_, errStr, _, err := ssh.Run(mkdircmd(target), p.Config.CommandTimeout)
				if err != nil {
					errChannel <- err
					return
				}

				if len(errStr) != 0 {
					errChannel <- fmt.Errorf(errStr)
					return
				}

				// untar file
				p.log(host, "untar file", p.DestFile)
				command := strings.Join(p.buildArgs(target), " ")
				if p.Config.Debug {
					fmt.Println("$", command)
				}
				outStr, errStr, _, err := ssh.Run(command, p.Config.CommandTimeout)

				if outStr != "" {
					p.log(host, "output: ", outStr)
				}

				if errStr != "" {
					p.log(host, "error: ", errStr)
				}

				if err != nil {
					errChannel <- err
					return
				}
			}

			// remove tar file
			err = p.removeDestFile(ssh)
			if err != nil {
				errChannel <- err
				return
			}

			wg.Done()
		}(host)
	}

	go func() {
		wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case err := <-errChannel:
		if err != nil {
			c := color.New(color.FgRed)
			_, theErr := c.Println("drone-scp error: ", err)
			if theErr != nil {
				return err
			}
			if _, ok := err.(copyError); !ok {
				fmt.Println("drone-scp rollback: remove all target tmp file")
				if err := p.removeAllDestFile(); err != nil {
					return err
				}
			}
			return err
		}
	}

	fmt.Println("===================================================")
	fmt.Println("✅ Successfully executed transfer data to all host")
	fmt.Println("===================================================")

	return nil
}
