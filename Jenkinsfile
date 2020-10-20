//
// Copyright (c) 2020 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

def notify = [ email: false, slack: [ success: '#ima-build-success', failure: '#ima-build-failed' ] ]

pipeline {
    agent { label 'edgex-testing' }
    triggers {
        cron('09 02 * * *')
    }

    options {
        timestamps()
        disableConcurrentBuilds() // To disable the concurrent builds
    }
    
    stages {
        // Changed the stage name to reflect you project
        stage('LLRP Test Execution') {
            environment {
                // this will expose SERVICE_TOKEN_USR and SERVICE_TOKEN_PSW in the build environment
                SERVICE_TOKEN = credentials('github-rsd-service-pat')
            }
            // Conditional trigger options
            when {
                anyOf {
                    triggeredBy cause: 'TimerTrigger'
                    triggeredBy cause: 'UserIdCause'
                }
            }
            steps {
                echo 'Start LLRP-Device test'
                sh 'docker-compose -f tests/docker-compose.yml up --build'
                archiveArtifacts allowEmptyArchive: true, artifacts: 'tests/reports/*.html', onlyIfSuccessful: true
                junit testResults: 'tests/reports/**/*.xml', allowEmptyResults: true
            }
        }
    }
    post {
        aborted {
            echo 'Test aborted'
        }
        success {
            // emailext body: "Build Succeeded\n Duration:${currentBuild.durationString}", to: "notifyme@myemail.com", subject: "Build ${currentBuild.number} Succeeded"
            script{
                slackBuildNotify {
                    slackSuccessChannel = notify.slack.success
                }
            }
        }
        failure {
            script{
                slackBuildNotify {
                    failed = true
                    slackFailureChannel = notify.slack.failure
                }
            }
            // emailext body: "Build Failed\n Duration:${currentBuild.durationString}", to: "notifyme@myemail.com", subject: "Build ${currentBuild.number} Failed"
        }
    }
} // pipeline
