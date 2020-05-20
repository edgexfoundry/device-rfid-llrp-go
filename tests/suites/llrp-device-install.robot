# @file LLRP Device install.robot
#    @author : Surajit Pal
#    @created:

*** Settings ***
| Documentation     | This file collects the Test Cases associated with the
| ...               | LLRP Device Service installtiom, and generally all Test Cases will
| ...               | be prefixed with their corresponding TC Number in Rally.
| Resource          | /scripts/llrp-device.resource                                                            |                                                  
| Library           | /scripts/install_check/container_count.py                                                |
| Suite Setup       | Suite_Setup                                                                              |
| Suite Teardown    | Suite_Teardown                                                                           |
| Test Setup        | Common_Setup                                                                             |
| Test Teardown     | Common_Teardown                                                                          |
| Variables         | /config/config.yaml                                                                      |

*** Variables ***


*** Keywords ***
| Suite_Setup
|    | Set Log Level                                                                        | DEBUG                                        |
|    | Log To Console                                                                       | ${SUITE NAME}: Suite Setup started.          |
|    | llrp-device.Clone LLRP Device Environment   | host_environment=${execution_environment}    | git_config=${git_config}                     |
|    | llrp-device.Build LLRP Device Environment                                                  | host_environment=${execution_environment}    |
|    | Sleep | 20s                                                                          |
#|    | llrp-device.Simulator LLRP Device Environment                                              | host_environment=${execution_environment}    |
#|    | Sleep | 10s                                                                          |
|    | Log To Console                                                                       | ${SUITE NAME}: Suite Setup finished.         |


| Common_Setup
|    | Log To Console    | \n${SUITE NAME}: Common Setup started.                    |   
|    | Log To Console    | ${SUITE NAME}: Common Setup finished.                     |   

| Common_Teardown
|    | Log To Console    | \n${SUITE NAME}: Common Teardown started.                 |  
|    | Log To Console    | ${SUITE NAME}: Common Teardown finished.                  |

| Suite_Teardown
|    | Log To Console                  | ${SUITE NAME}: Suite Teardown started.       |
#|    | llrp-device.Shutdown LLRP Simulator Environment    | host_environment=${execution_environment}    |
|    | llrp-device.Shutdown LLRP Device Environment              | host_environment=${execution_environment}    |
|    | Log To Console                  | ${SUITE NAME}: Suite Teardown finished.      |

*** Test cases ***
| TC0001_Execute_LLRP_Device_Service_Test
|    | [Tags]             | Run                                       | generic | install
|    | [Documentation]    | LLRP Device Installtion Check             |
|    | Log To Console     | Testcase ${TEST NAME} started.            |
|    | ${status}=         | verify_containers_running | positive      |
|    | Log To Console     | Status = ${status}            |
|    | Should Be True     | ${status}                                 |
|    | Log To Console     | Testcase ${TEST NAME} finished.           |
