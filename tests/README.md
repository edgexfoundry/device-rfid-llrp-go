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




Local Build Command :

  >>  docker-compose -f docker-compose.yml up --build

tests/.gitignore

    This sample of items that should be in your .gitignore file and one line that is a must for you. The line you must have is **/.env. The .env file will contain your credential to access git hub and must not be committed to github. If you plan on doing local debug of your tests then you will need to populate this file.

    NOTE: If you accidentally commit and push this file undo the commit and clean up the history. The DevOps team does not want to fail an audit it a credential is found in the a repo

tests/.env

    If you are planning on debugging the tests locally the values below are needed since Jenkins will not supply them.
        SERVICE_TOKEN= <Place the key you generate in github here.>
        GIT_BRANCH= <Your local branch.>
        http_proxy= http://proxy-chain.intel.com:911 <If you are running on a linux machine on the Intel network>
        https_proxy= http://proxy-chain.intel.com:912 <If you are running on a linux machine on the Intel network>

        NOTE: Do not push this file to your repo.

