/*
Copyright 2017 Mario Kleinsasser and Bernhard Rausch

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

var mainloop bool

type Message struct {
	Acode   int64
	Astring string
	Aslice  []string
}

type Backend struct {
	Node string
	Port string
}

func isprocessrunningps(processname string) (running bool) {

	// get all folders from proc filesystem

	files, _ := ioutil.ReadDir("/proc")
	for _, f := range files {

		// check if folder is a integer (process number)
		if _, err := strconv.Atoi(f.Name()); err == nil {
			// open status file of process
			f, err := os.Open("/proc/" + f.Name() + "/status")
			if err != nil {
				log.Println(err)
				return false
			}

			// read status line by line
			scanner := bufio.NewScanner(f)

			// check if process name in status of process
			for scanner.Scan() {

				re := regexp.MustCompile("^Name:.*" + processname + ".*")
				match := re.MatchString(scanner.Text())

				if match == true {
					return true
				}

			}

		}
	}

	return false

}

func startprocess() {
	log.Print("Start Process!")
	cmd := exec.Command("nginx", "-g", "daemon off;")
	err := cmd.Start()
	if err != nil {
		log.Fatal(err)
		mainloop = false
	}

}

func reloadprocess() {
	log.Print("Reloading Process!")
	cmd := exec.Command("nginx", "-s", "reload")
	err := cmd.Start()
	if err != nil {
		log.Fatal(err)
	}
	cmd.Wait()
}

func checkconfig(be []string, domain string) (changed bool) {

	// first sort the string slice
	sort.Strings(be)

	var data []Backend

	for _, e := range be {
		var t Backend
		et := strings.Split(e, " ")
		t.Node = et[0] + "." + domain
		t.Port = et[1]
		data = append(data, t)
	}

	return writeconfig(data)

}

func checkconfigdns(be []string, port string) (changed bool) {

	// first sort the string slice
	sort.Strings(be)

	var data []Backend

	for _, e := range be {
		var t Backend
		t.Node = e
		t.Port = port
		data = append(data, t)
	}

	return writeconfig(data)
}

func writeconfig(data []Backend) (changed bool) {

	//  open template
	t, err := template.ParseFiles("/config/border-controller-config.tpl")
	if err != nil {
		log.Print(err)
		return false
	}

	// process template
	var tpl bytes.Buffer
	err = t.Execute(&tpl, data)
	if err != nil {
		log.Print(err)
		return false
	}

	// create md5 of result
	md5tpl := fmt.Sprintf("%x", md5.Sum([]byte(tpl.String())))
	log.Print("MD5 of TPL: " + md5tpl)
	log.Print("TPL: " + tpl.String())

	// open existing config, read it to memory
	exconf, errexconf := ioutil.ReadFile("/etc/nginx/nginx.conf")
	if errexconf != nil {
		log.Print("Cannot read existing conf!")
		log.Print(errexconf)
	}

	md5exconf := fmt.Sprintf("%x", md5.Sum(exconf))
	log.Print("MD5 of EXCONF: " + md5exconf)

	// comapre md5 and write config if needed
	if md5tpl == md5exconf {
		log.Print("MD5 sums equal! Nothing to do.")
		return false
	}

	log.Print("MD5 sums different writing new conf!")

	// overwrite existing conf
	err = ioutil.WriteFile("/etc/nginx/nginx.conf", []byte(tpl.String()), 0644)
	if err != nil {
		log.Print("Cannot write config file.")
		log.Print(err)
		mainloop = false
	}

	return true

}

func getstackerviceinfo(config T) (backends []string, err error) {

	var m Message

	for _, dh := range config.General.Swarm.Docker_hosts {
		log.Print(dh)

		resp, err := http.Get("http://" + dh + "." +
			config.General.Swarm.Docker_host_dns_domain + ":" +
			config.General.Swarm.Docker_controller.Exposed_port +
			"/service/inspect/" + config.General.Swarm.Ingress_service_name +
			"?api_key=" + config.General.Swarm.Docker_controller.Api_key)

		if err != nil {
			log.Print(err)
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode == 200 { // OK
			bodyBytes, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return nil, errors.New("Error reading response body")
			}

			err = json.Unmarshal(bodyBytes, &m)
			if err != nil {
				return nil, errors.New("Error reading during unmarshal of response body.")
			}

			if m.Acode >= 500 {
				return nil, errors.New(strconv.Itoa(int(m.Acode)) + " " + m.Astring)
			}
		}

		return m.Aslice, nil

	}

	return nil, errors.New("Cannot reach any docker host")

}

func getstacktaskdns(config T) (backends []string, err error) {

	if config.General.Swarm.Stack_service_port == "" {
		log.Panic("No Swarm Service Port given! Exiting!")
	}

	// resolve fiven service names

	servicerecords, err := net.LookupHost(config.General.Swarm.Stack_service_task_dns_name)

	if err != nil {
		return nil, err
	}

	return servicerecords, nil

}

func main() {

	config, ok := ReadConfigfile()
	if !ok {
		log.Panic("Error during config parsing")
	}

	// now checkconfig, this will loop forever
	mainloop = true
	for mainloop == true {

		var changed bool

		if config.General.Swarm.Stack_service_task_dns_name != "" && config.General.Swarm.Ingress_service_name != "" {
			log.Panic("Stack Service Task DNS and Ingress Service Name configured! Exiting!")
		}

		if config.General.Swarm.Ingress_service_name != "" {
			backends, err := getstackerviceinfo(config)
			log.Print(backends)
			if err != nil {
				log.Print(err)
				time.Sleep(5 * time.Second)
				continue
			}

			changed = checkconfig(backends, config.General.Swarm.Docker_host_dns_domain)

		} else if config.General.Swarm.Stack_service_task_dns_name != "" {
			backends, err := getstacktaskdns(config)
			log.Print(backends)
			if err != nil {
				log.Print(err)
				time.Sleep(5 * time.Second)
				continue
			}

			changed = checkconfigdns(backends, config.General.Swarm.Stack_service_port)

		} else {
			log.Panic("No Service Descovery configured!")
		}

		if changed == true {
			if isprocessrunningps("nginx") {
				reloadprocess()
			} else {
				startprocess()
			}
		} else {
			if !isprocessrunningps("nginx") {
				startprocess()
			}
		}

		time.Sleep(10 * time.Second)
	}
}
