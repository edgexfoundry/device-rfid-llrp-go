import sys
from pyral import Rally, rallyWorkset
options = [arg for arg in sys.argv[1:] if arg.startswith('--')]
args = [arg for arg in sys.argv[1:] if arg not in options]
# server, user, password, apikey, workspace, project = rallyWorkset(options)
server = "rally1.rallydev.com"
user = ""
password = ""
workspace = "IOTG/RSD"
project = "Go Ninjas Team"
rally = Rally(server, user, password, apikey=os.environ.get('RALLY_TOKEN'),
              workspace=workspace, project=project)
rally.enableLogging('mypyral.log')


# def GetTestCases():
query_criteria = ''
response = rally.get('TestCase', fetch=True)
if response.errors:
    sys.stdout.write("\n".join(errors))
    sys.exit(1)
for testCase in response:  # there should only be one qualifying TestCase
    print "%s %s %s %s" % (testCase.Name, testCase.Type,
                            testCase.DefectStatus, testCase.LastVerdict)
