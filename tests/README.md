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
│       └── textutils.sh
└── version.go
```

# Local Build Command 

  At Path "device-llrp-go/tests/"

  docker-compose -f docker-compose.yml up --build
  

# tests/.env
    
For tests locally, Then create a file "/tests/.env" and added below these two lines -

SERVICE_TOKEN= <Place the key you generate in github here.>
GIT_BRANCH= <Your local branch.>


NOTE: Do not push this file to your repo.

