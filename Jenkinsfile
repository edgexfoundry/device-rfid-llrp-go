def notify = [ email: false, slack: [ success: '#ima-build-success', failure: '#ima-build-failure' ] ]

pipeline {
    agent { label 'edgex-testing' }
    triggers {
        cron('07 02 * * *')
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
