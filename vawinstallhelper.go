package main

import (
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/registry"
	"log"
	"os"
	"os/exec"
	"fmt"
	"strings"
	"path/filepath"
	"time"
	"errors"
)

func exePath() (string, error) {
	prog := os.Args[0]
	p, err := filepath.Abs(prog)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err == nil {
		if !fi.Mode().IsDir() {
			return p, nil
		}
		err = fmt.Errorf("%s is directory", p)
	}
	if filepath.Ext(p) == "" {
		p += ".exe"
		fi, err := os.Stat(p)
		if err == nil {
			if !fi.Mode().IsDir() {
				return p, nil
			}
			err = fmt.Errorf("%s is directory", p)
		}
	}
	return "", err
}

func installService(name, desc string) error {
	exepath, err := exePath()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}
	s, err = m.CreateService(name, exepath, mgr.Config{DisplayName: desc})
	if err != nil {
		return err
	}
	defer s.Close()
	err = eventlog.InstallAsEventCreate(name, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil {
		s.Delete()
		return fmt.Errorf("SetupEventLogSource() failed: %s", err)
	}
	return nil
}

func removeService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", name)
	}
	defer s.Close()
	err = s.Delete()
	if err != nil {
		return err
	}
	err = eventlog.Remove(name)
	if err != nil {
		return fmt.Errorf("RemoveEventLogSource() failed: %s", err)
	}
	return nil
}

func startService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err)
	}
	defer s.Close()
	err = s.Start("is", "manual-started")
	if err != nil {
		return fmt.Errorf("could not start service: %v", err)
	}
	return nil
}

func statusService(name string) (error,svc.State) {
	m, err := mgr.Connect()
	if err != nil {
		return err,svc.Stopped
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err),svc.Stopped
	}
	defer s.Close()
	
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("could not query service: %v", err),svc.Stopped
	}
	return nil,status.State
}

func controlService(name string, c svc.Cmd, to svc.State) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err)
	}
	defer s.Close()
	status, err := s.Control(c)
	if err != nil {
		return fmt.Errorf("could not send control=%d: %v", c, err)
	}
	timeout := time.Now().Add(10 * time.Second)
	for status.State != to {
		if timeout.Before(time.Now()) {
			return fmt.Errorf("timeout waiting for service to go to state=%d", to)
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("could not retrieve service status: %v", err)
		}
	}
	return nil
}

type VAWInstallService struct{
	elog * debug.Log
}
func (v *VAWInstallService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	logger := (*(v.elog))

	const cmdsAccepted = 0
	
	changes <- svc.Status{State: svc.StartPending}
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	start := time.Now() 
	
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\VeeamHub`, registry.QUERY_VALUE)
	defer k.Close()
	if err == nil {
		srcinstall, _, err := k.GetStringValue("VAWExecSrc")
		if err == nil {
			logger.Info(1010, fmt.Sprintf("Installing from %s",srcinstall))
			
			cmd := exec.Command(srcinstall, "/silent","/accepteula")
			err := cmd.Start()
			if err == nil {
				logger.Info(1011,"Exec Started")
				err = cmd.Wait()
				if err == nil {
					logger.Info(1012,"Exec Finished")
				} else {
					logger.Error(2003,fmt.Sprintf("Error after running install %v",err))
				}			
			} else {
				logger.Error(2002,fmt.Sprintf("Error running install %v",err))
			}	
		} else {
			logger.Error(2001,`Could not find string VAWExecSrc in HKLM:\SOFTWARE\VeeamHub`)
		}
	} else {
		logger.Error(2001,`Could not find key HKLM:\SOFTWARE\VeeamHub`)
	}
	
	
	
	proctime := time.Since(start).Seconds()
	if proctime < 5 {
		time.Sleep(5 * time.Second)
		(*(v.elog)).Info(1003, fmt.Sprintf("Sleeping because only did %f of work",proctime))
	} else {
		(*(v.elog)).Info(1004, fmt.Sprintf("Executed in %f seconds",proctime))
	}
	
	changes <- svc.Status{State: svc.StopPending}
	return
}

func runService(name string, isDebug bool) {
	var err error
	var elog debug.Log
	
	if isDebug {
		elog = debug.New(name)
	} else {
		elog, err = eventlog.Open(name)
		if err != nil {
			return
		}
	}
	defer elog.Close()

	elog.Info(1001, fmt.Sprintf("starting %s service", name))
	
	run := svc.Run
	if isDebug {
		run = debug.Run
	}
	err = run(name,&VAWInstallService{&elog})
	
	elog.Info(1002, fmt.Sprintf("%s service stopped", name))
}

func waitForServiceToStop(name string,timeoutSec int) error {
		m, err := mgr.Connect()
		if err != nil {
			return err
		}
		defer m.Disconnect()
		
		s, err := m.OpenService(name)
		if err != nil {
			return fmt.Errorf("could not access service: %v", err)
		}
		defer s.Close()
		
		timeout := time.After(time.Duration(timeoutSec) * time.Second)
		tick := time.Tick(1 * time.Second)

		for {
			select {
				case <-timeout:
					return errors.New("Timed out")
				case <-tick:
					status, err := s.Query()
					if err != nil {
						return fmt.Errorf("could not query service: %v", err)
					} 
					
					if status.State == svc.Stopped {
						return nil
					}
			}
		}
		
		return errors.New("Unable to reach error")
} 

func mkInstallPathKey(path string) error {
	strkeyname := "VAWExecSrc"
	strkeypath := `SOFTWARE\VeeamHub`
	
	mk,_, err := registry.CreateKey(registry.LOCAL_MACHINE, strkeypath, registry.ALL_ACCESS)
	defer mk.Close()
	
	if err == nil {
		err = mk.SetStringValue(strkeyname, path) 
	}
	
	return err
}

func main() {
	const svcName = "vawinstallhelper"
	const svcDisplayName = "VAW Install Helper"
	
	var err error
	
	if len(os.Args) < 2 {
		isIntSess, err := svc.IsAnInteractiveSession()
		if err != nil {
			//log.Printf("failed to determine if we are running in an interactive session: %v", err)
			isIntSess = false
		}

		if isIntSess {
			log.Printf("Is interactive, running debug")
			runService(svcName, true)
		} else {
			runService(svcName, false)
		}
	} else {
		cmd := strings.ToLower(os.Args[1])
		switch cmd {
			case "install":
				err = installService(svcName, svcDisplayName)
			case "remove":
				err = removeService(svcName)
			case "start":
				err = startService(svcName)
			case "installstart":
				err = installService(svcName, svcDisplayName)
				if (err == nil) {
					err = startService(svcName)
				}
			case "deploy":
				err = installService(svcName, svcDisplayName)
				if (err == nil) {
					err = startService(svcName)
					if err == nil {
						err = waitForServiceToStop(svcName,500)
						if err == nil {
							err = removeService(svcName)
							if err == nil {
								fmt.Printf("--StoppedRemoved");
							}
						}	
					}
				}
			case "mkregkey":
				if len(os.Args) > 2 {
					err = mkInstallPathKey(os.Args[2])
				} else {
					err = errors.New("Need second argument")
				}
			case "status":
				var state svc.State
				err,state = statusService(svcName)
				if (err == nil) {
					if (state != svc.Running) {
						fmt.Printf("--Not running (%d)",state)
					} else {
						fmt.Printf("--Running")
					}
				}
		}
		if err != nil {
			log.Printf("Something went wrong %s",err)
			os.Exit(2)
		}
	}
}