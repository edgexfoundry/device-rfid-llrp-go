# FAST - Framework for Simplified Testing

## Basic file layout for test infrastructure for LLRP Device Service.

```
└── device-llrp-go
├── <Note: LLRP Device Service Soruce/files >
├── Jenkinsfile
├── tests [FAST]
│   ├── config
│   │   └── config.yaml
│   ├── docker-compose.yml
│   ├── DockerfileRobot
│   ├── README.md
│   ├── requirements.txt
│   ├── scripts
│   │   ├── install_check
│   │   │   └── container_count.py
│   │   ├── lltp-device.resource
│   │   ├── rally.py
│   │   └── TM-GenerateProjectData.sh
│   ├── suites
│   │   └── llrp-device-install.robot
│   └── utils
│       ├── RemoveDockerImages.sh
│       ├── InstallDockerPkgDep.sh
│       └── textutils.sh
└── version.go
```

## How do I build locally in my repo?

# 1. tests/.env 
NOTE: Do not push this file to your repo/branch.
    
For tests locally, Create a file "/tests/.env" and add below these two lines -

    SERVICE_TOKEN=<Place your git token here>
    GIT_BRANCH=<Your local branch>

    e.g : `FAST` is branch name, then GIT_BRANCH=FAST

    `docker-compose config` - command to check the all env and args.


# 2. Build Command 

  Goto at path `device-llrp-go/tests/`

  `docker-compose -f docker-compose.yml up --build`


## How do I verify the build result/reports?

  For the test results open `reports.html` in `/tests/reports`.
  Console logs also.



## Remotely
# How do I build remotely at jenkins agent?
 
Build: [![Build Status](https://rrpdevops01.amr.corp.intel.com/job/RSP-Inventory-Suite/job/device-llrp-go/view/change-requests/job/PR-3/)](https://rrpdevops01.amr.corp.intel.com/job/RSP-Inventory-Suite/job/device-llrp-go/view/change-requests/job/PR-3/)

Navigate to `https://rrpdevops01.amr.corp.intel.com/job/RSP-Inventory-Suite/job/device-llrp-go/`
