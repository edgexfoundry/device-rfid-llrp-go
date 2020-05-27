# Copyright (C) 2020 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

# @file llrp device.resource.robot

*** Settings ***
| Documentation    |
| ...              | This is a Robot resource file for LLRP Device Service
| ...              | collection common build-type LLRP Device Service with 
| ...              | meaningful Keywords.
| Library          | DateTime                                                        
| Library          | Process                                                         
| Library          | OperatingSystem 



*** Variables ***



*** Keywords ***

####=============================================================####

| Clone LLRP Device Environment
|                 | [Arguments]                 | ${git_config}=                                                              | ${host_environment}=                                     |
|                 | ${start_time}=              | Get Time                                                                    |
|                 | ${start_time}=              | Convert Date                                                                | ${start_time}                                            | epoch |
|                 | Log to Console              | Clone LLRP Device Environment                                               |
|                 | Should Match RegExp         | ${host_environment['working_dir']}                               | /tmp/.+  | Working Directories should be nested in the `/tmp` directory    |
|                 | ${result}= | Run Process    | rm -fr ${host_environment['working_dir']}                                                          | shell=true |
|                 | Log to Console              | ${result.stdout}                                                 | ${result.stderr}           |
|                 | ${result}= | Run Process    | mkdir -p ${host_environment['working_dir']}                                                         | shell=true |
|                 | Log to Console              | ${result.stdout}                                                 | ${result.stderr}           |
| | ${result}=    | Run Process                 | git config --global credential.helper store                                                         | shell=true | cwd=${host_environment['working_dir']} |
|                 | Log to Console              | ${result.stdout}                                                 | ${result.stderr}           |
| | ${result}=    | Run Process                 | set +x && echo "https://%{SERVICE_TOKEN}:x-oauth-basic@github.impcloud.net" > ~/.git-credentials    | shell=true |
|                 | Log to Console              | ${result.stdout}                                                 | ${result.stderr}           |
| | ${result}=    | Run Process                 | git clone ${git_config['repo']} --branch ${git_config['branch']}                                    | shell=true | cwd=${host_environment['working_dir']} |
|                 | Log to Console              | ${result.stdout}                                                 | ${result.stderr}           |

####=============================================================####

| Build LLRP Device Environment
|                   | [Arguments]            | ${git_config}=                        | ${host_environment}=    |
|                   | ${start_time}=         | Get Time                              |
|                   | ${start_time}=         | Convert Date                          | ${start_time}           | epoch |
|                   | Log to Console         | Build LLRP Device Environment            |
|                   | Should Match RegExp    | ${host_environment['working_dir']}    | /tmp/.+                 | Working Directories should be nested in the `/tmp` directory      |
| | ${result}=      | Run Process            | ls "-R"                                   | shell=true              | cwd=${host_environment['working_dir']}            |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | ${result}=      | Run Process            | ./installPkg.sh                       | shell=true              | cwd=${host_environment['working_dir']}/device-llrp-go/tests/utils  |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | ${result}=      | Run Process            | make docker                           |  shell=true             | cwd=${host_environment['working_dir']}/device-llrp-go              |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | Log To Console  | \n -----: Dockcer Build Env Setup is started...Take few minutes- :----\n  |
| | ${result}=      | Run Process            | docker-compose "-f" "docker-compose-geneva-redis-no-secty.yml" "up" "-d" | shell=true  |  stdout=DEVNULL | stderr=DEVNUL  | cwd=${host_environment['working_dir']}/device-llrp-go     | 
#|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | Log To Console  | \n -----: Ddocker Containers :----\n  |
| | ${result}=      | Run Process            | docker "ps" "--format" "{{.Names}}"   | shell=true              | cwd=${host_environment['working_dir']}/device-llrp-go     |
|                   | Log to Console         |  ${result.stdout}                     | ${result.stderr}        |
|                   | Log to Console         |  \n LLRP Device Build Done.....\n          |
|                   | Log to Console         |  \n After Build wait for 10s.....\n   |

####=============================================================####

| Simulator LLRP Device Environment
|                 | [Arguments]            | ${git_config}=                         | ${host_environment}=    |
|                 | ${start_time}=         | Get Time                               |
|                 | ${start_time}=         | Convert Date                           | ${start_time}           | epoch |
|                 | Log to Console         | Simulator LLRP Service Environment         |
|                 | Log to Console         | \n LLRP Device Service Simulating......\n      |
|                 | Should Match RegExp    | ${host_environment['working_dir']}     | /tmp/.+                 | Working Directories should be nested in the `/tmp` directory        |
#| | ${result}=    | Run Process            | docker ps --format  {{.Names}}        | shell=true              | cwd=${host_environment['working_dir']}     |
#|                 | Log to Console         | ${result.stdout}                      | ${result.stderr}        |


####=============================================================####

| Shutdown LLRP Simulator Environment
|                 | [Arguments]            | ${git_config}=                         | ${host_environment}=    |
|                 | ${start_time}=         | Get Time                               |
|                 | ${start_time}=         | Convert Date                           | ${start_time}           | epoch |
|                 | Log to Console         | Shutdown LLRP Device Service Simulator Environment |


####=============================================================####

| Shutdown LLRP Device Environment
|                 | [Arguments]            | ${git_config}=                              | ${host_environment}=    |
|                 | ${start_time}=         | Get Time                                    | 
|                 | ${start_time}=         | Convert Date                                | ${start_time}           | epoch |
|                 | Log to Console         | Shutdown LLRP Device Environment...            |
|                 | Should Match RegExp    | ${host_environment['working_dir']}          | /tmp/.+                 | Working Directories should be nested in the `/tmp` directory    |
#| | ${result}=    | Run Process            | docker container stop $( docker container ls  | grep "llrp" | awk '{print $1}')  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
#|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
| | ${result}=    | Run Process            | docker-compose -f docker-compose-geneva-redis-no-secty.yml down  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
| | ${result}=    | Run Process            | ./rmvDockerImgs.sh        | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go/tests/utils |
|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
#| | ${result}=    | Run Process            | docker images -a  | grep "edgexfoundry"     | awk '{print $3}' | xargs docker rmi -f | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
#|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
#| | ${result}=    | Run Process            | docker "rmi" "$(docker images -a -q)"       | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
#|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
#| | ${result}=    | Run Process            | docker rmi $(docker images -f dangling=true -q)  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
#|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
|                 | Log to Console          |   NOTE:: ALL Shutdown Completed            |
#| | ${result}=    | Run Process            | sduo rm -rf device-llrp-go                 | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | sudo docker network prune -f               | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | docker volume ls -f dangling=true          | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | sudo docker-compose -f docker-compose.yml down     | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go/tests |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |

