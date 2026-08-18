pipeline {
  agent { label 'py-3.8-ub24-sm' }
  environment {
    // Added CANAVERAL build variables from YAML
    CANAVERAL_DEPS = "jq"
    CANAVERAL_BUILD_SYSTEM = "noop"
    CANAVERAL_PACKAGE_TOOLS = "docker"
    DOCKER_REPO_SERVER = "artifactory.dyn.ntnxdpro.com/canaveral-legacy-docker"
    CANAVERAL_LANGUAGE_VERSION = "3.8.5"

    // Existing environment variables
    CANAVERAL_ARTIFACTORY_READ_USER = credentials('CANAVERAL_ARTIFACTORY_READ_USER')
    CANAVERAL_ARTIFACTORY_READ_PASSWORD = credentials('CANAVERAL_ARTIFACTORY_READ_PASSWORD')
    TOKEN_VENDING_MACHINE_HASH = credentials('TOKEN_VENDING_MACHINE_HASH')
    USE_ARTIFACTORY = credentials('USE_ARTIFACTORY')
    CANAVERAL_USE_LEGACY_PIPELINE_MODE = 0
    CANAVERAL_USE_BLUEPRINT_PROXY = 1
    CAN_JENKINS = "true"
  }

  stages {
    stage('Install Dependencies') {
      steps {
        sh(label: 'Install Dependencies', script: '''
          sudo apt-get update
          sudo apt-get install -y --no-install-recommends $CANAVERAL_DEPS
        ''')
      }
    }
    stage('Install Build Tools') {
      steps {
        script {
          installBuildTools()
        }
      }
    }
    stage('Setup Environment') {
      steps {
        script {
          pyenvSh('Run setup environment', 'python -m canaveral_build_tools.core.setup_environment')
          def descFile = "${env.WORKSPACE}/.workspace/.canaveral_build_description"
          if (fileExists(descFile)) {
            currentBuild.description = readFile(descFile).trim()
          }
        }
      }
    }
    stage('Dependency Setup') {
      steps {
        script {
          pyenvSh('Run dependency setup', 'python -m canaveral_build_tools.core.dependency_setup')
        }
      }
    }

    stage('Database Setup') {
      steps {
        script {
          pyenvSh('Run database setup', 'python -m canaveral_build_tools.core.setup_database')
        }
      }
    }

    stage('Push event Build::Started') {
      steps {
        script {
          pyenvSh('Push Build::Started event', 'python -m canaveral_build_tools.core.push_event Build::Started')
        }
      }
    }

    stage('Build') {
      steps {
        script {
          pyenvSh('Run build', 'python -m canaveral_build_tools.core.build')
        }
      }
    }

    stage('Unit tests') {
      steps {
        script {
          pyenvSh('Run unit tests', 'python -m canaveral_build_tools.core.run_tests "unit"')
        }
      }
    }
    stage('Package') {
      steps {
        script {
          pyenvSh('Run package', 'python -m canaveral_build_tools.core.package')
        }
      }
    }
  }
}