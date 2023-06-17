package main

import (
	"embed"
	"flag"
	"io/fs"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strings"
	"syscall"

	"github.com/gorilla/mux"
	"github.com/skratchdot/open-golang/open"
)

//latest-tag for seva-browser
var docker_browser_tag = "latest"
var docker_browser_path = "ghcr.io/cshilwant/seva-browser:"+docker_browser_tag

// path to seva-browser.tar.gz in tisdk-default-image
var path_to_docker_browser = "/opt/seva-browser.tar.gz"

var store_url = "https://raw.githubusercontent.com/cshilwant/seva-apps/main"

var addr = flag.String("addr", "0.0.0.0:8000", "http service address")
var no_browser = flag.Bool("no-browser", false, "do not launch browser")
var docker_browser = flag.Bool("docker-browser", false, "force use of docker browser")
var http_proxy = flag.String("http_proxy", "", "use to set http proxy")
var no_proxy = flag.String("no_proxy", "", "use to set no-proxy")

var container_id_list [2]string
var docker_compose string

//go:embed web/*
var content embed.FS

//go:embed docker-compose
var docker_compose_bin []byte

// Checks if docker compose is present in the file-system
func is_docker_compose_installed() bool {
	cmd := exec.Command("docker-compose", "-v")
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Println("Docker-compose is either not installed or cannot be executed")
		log.Println(err)
		log.Println("Using local install for now")
		return false
	}
	return true
}

func prepare_compose() string {
	if !is_docker_compose_installed() {
		ioutil.WriteFile("docker-compose", docker_compose_bin, 0755)
		return "./docker-compose"
	}
	return "docker-compose"
}

func setup_working_directory() {
	err := os.MkdirAll("/tmp/seva-launcher", os.ModePerm)
	if err != nil {
		log.Println(err)
		exit(1)
	}
	err = os.Chdir("/tmp/seva-launcher")
	if err != nil {
		log.Println(err)
		exit(1)
	}
}

func launch_browser() {
	if *docker_browser {
		go launch_docker_browser()
	} else {
		err := open.Start("http://localhost:8000/#/")
		if err != nil {
			log.Println("Host browser not detected, trying to load & launch seva-browser packaged in default image")
			go launch_docker_browser()
		}
	}
}

// Generates a docker image for seva-browser from tar.gz
func generate_docker_browser(args ...string) {
	args = append([]string{"load"}, args...)
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	log.Printf("|\n%s\n", output)

	if err != nil {
		log.Println("seva-browser packaged in default image didn't load, fetching one through docker")
		return
	}
}

// Checks if seva-browser is packaged inside tisdk-default-image
func browser_image_present() bool {
	_, err := os.Stat(path_to_docker_browser)
	if os.IsNotExist(err) {
		log.Println("seva-browser doesn't exist in default image, fetching one through docker")
		return false
	}
	return true
}

// Checks if seva-browser is extracted from tar.gz to docker image
func is_browser_loaded() bool {
	imageName := "seva-browser"
	cmd := exec.Command("docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	log.Println("Docker image ls output is %s\n", string(output))

	images := strings.Split(string(output), "\n")
	for _, tag := range images {
		log.Println(tag)
		if tag == docker_browser_path {
			log.Println("Found image %s\n", tag)
			return true
		}
	}

	log.Println("Image %s not found\n", imageName)
	return false
}

func launch_docker_browser() {
	xdg_runtime_dir := os.Getenv("XDG_RUNTIME_DIR")
	user, _ := user.Current()
        log.Println(xdg_runtime_dir)
        log.Println(user.Uid)
        log.Println(user.Gid)

	if browser_image_present() && !is_browser_loaded() {
		generate_docker_browser("--input", path_to_docker_browser)
	}
	output := docker_run("--rm", "--privileged", "--network", "host",
		"-e", "XAUTHORITY",
		"-e", "XDG_RUNTIME_DIR=/tmp",
		"-e", "DISPLAY",
		"-e", "WAYLAND_DISPLAY",
		"-e", "https_proxy",
		"-e", "http_proxy",
		"-e", "no_proxy",
		"-v", xdg_runtime_dir+":/tmp",
		"--user="+user.Uid+":"+user.Gid,
		"ghcr.io/cshilwant/seva-browser:"+docker_browser_tag,
		"http://localhost:8000/#/",
	)
	output_strings := strings.Split(strings.TrimSpace(string(output)), "\n")
	container_id_list[1] = output_strings[len(output_strings)-1]
}

func docker_run(args ...string) []byte {
	args = append([]string{"run", "-d"}, args...)
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	log.Printf("|\n%s\n", output)
	if err != nil {
		log.Println("Failed to start container!")
		log.Println(err)
		exit(1)
	}
	return output
}

func start_design_gallery() {
	log.Println("Starting local design gallery service")
	output := docker_run("--rm", "-p", "8001:80",
		"ghcr.io/cshilwant/seva-design-gallery:latest",
	)
	output_strings := strings.Split(strings.TrimSpace(string(output)), "\n")
	container_id_list[0] = output_strings[len(output_strings)-1]
}

func exit(num int) {
	log.Println("Stopping non-app containers")
	for _, container_id := range container_id_list {
		if len(container_id) > 0 {
			cmd := exec.Command("docker", "stop", container_id)
			output, err := cmd.CombinedOutput()
			if err != nil {
				log.Printf("Failed to stop container %s : \n%s", container_id, output)
			}
		}
	}
	os.Exit(num)
}

func setup_exit_handler() {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		exit(0)
	}()
}

func handle_requests() {
	router := mux.NewRouter()
	router.HandleFunc("/ws", websocket_controller)
	log.Println("Listening for websocket messages at " + *addr + "/ws")
	root_content, err := fs.Sub(content, "web")
	if err != nil {
		log.Println("No files to server for web interface!")
		exit(1)
	}
	router.PathPrefix("/").Handler(http.FileServer(http.FS(root_content)))
	log.Println(http.ListenAndServe(*addr, router))
}

func check_env_vars() {
	for _, element := range []string{"DISPLAY", "WAYLAND_DISPLAY"} {
		env_var := os.Getenv(element)
		if len(env_var) > 0 {
			return
		}
	}
	log.Println("Environment variable DISPLAY or WAYLAND_DISPLAY must be set!")
	exit(1)
}

func valid_proxy() bool {
	_, err := url.ParseRequestURI(*http_proxy)
	return err == nil
}

func setup_proxy() {
	// Setting up Environment Variables
	// If http_proxy is valid apply changes to Environment variable
	if *http_proxy == "" && *no_proxy == "" {
		// TODO: Revert proxy settings
	} else if valid_proxy() {
		proxy_settings := ProxySettings{
			HTTPS: *http_proxy,
			HTTP:  *http_proxy,
			FTP:   *http_proxy,
			NO:    *no_proxy,
		}
		apply_proxy_settings(proxy_settings)
	} else {
		log.Println("Invalid proxy given, ignoring proxy settings!")
	}
}

func main() {
	setup_exit_handler()
	check_env_vars()
	flag.Parse()

	setup_proxy()

	log.Println("Setting up working directory")
	setup_working_directory()
	docker_compose = prepare_compose()

	if !*no_browser {
		log.Println("Launching browser")
		launch_browser()
	}

	handle_requests()
}
