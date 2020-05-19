# @file llrp device.resource.robot
#    @author : Surajit Pal
#    @created: 

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
|                   | Should Match RegExp    | ${host_environment['working_dir']}    | /tmp/.+                 | Working Directories should be nested in the `/tmp` directory     |
| | ${result}=      | Run Process            | ls                                    | shell=true              | cwd=${host_environment['working_dir']}/device-llrp-go            |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | Log To Console  | \n: Build Env Setup is started...Take few minutes- \n          |
| | ${result}=      | Run Process            | make docker                           |  shell=true             | cwd=${host_environment['working_dir']}/device-llrp-go             |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | ${result}=      | Run Process            | docker-compose -f docker-compose-geneva-redis-no-secty.yml up -d |  shell=true             | cwd=${host_environment['working_dir']}/device-llrp-go             |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
| | ${result}=      | Run Process            | docker ps                             | shell=true              | cwd=${host_environment['working_dir']}/device-llrp-go     |
|                   | Log to Console         | ${result.stdout}                      | ${result.stderr}        |
|                   | Log to Console         |  LLRP Device Build Done.....             |
|                   | Log to Console         |  \n After Build wait for 30s.....\n   |

####=============================================================####

| Simulator LLRP Device Environment
|                 | [Arguments]            | ${git_config}=                         | ${host_environment}=    |
|                 | ${start_time}=         | Get Time                               |
|                 | ${start_time}=         | Convert Date                           | ${start_time}           | epoch |
|                 | Log to Console         | Simulator LLRP Service Environment         |
|                 | Log to Console         | \n LLRP Device Service Simulating......\n      |
|                 | Should Match RegExp    | ${host_environment['working_dir']}     | /tmp/.+                 | Working Directories should be nested in the `/tmp` directory        |


####=============================================================####

| Shutdown LLRP Device Environment
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
| | ${result}=    | Run Process            | sudo docker container stop $(sudo docker container ls  | grep "llrp" | awk '{print $1}')  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
| | ${result}=    | Run Process            | sudo docker-compose -f docker-compose-geneva-redis-no-secty.yml down  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
|                 | Log to Console         | ${result.stdout}                            | ${result.stderr}        |
| | ${result}=    | Run Process            | docker images -a | grep "llrp" | awk '{print $3}' | xargs sudo docker rmi  | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go |
|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
|                 | Log to Console          |   NOTE:: ALL Shutdown Completed            |
#| | ${result}=    | Run Process            | sduo rm -rf device-llrp-go              | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | sudo docker network prune -f               | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | sudo docker volume prune -f                | shell=true              | cwd=${host_environment['working_dir']} |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |
#| | ${result}=    | Run Process            | sudo docker-compose -f docker-compose.yml down     | shell=true | cwd=${host_environment['working_dir']}/device-llrp-go/tests |
#|                 | Log to Console         | ${result.stdout}                           | ${result.stderr}        |

