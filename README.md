# Device LLRP Go
[![license](https://img.shields.io/badge/license-Apache%20v2.0-blue.svg)](LICENSE)
## Overview
LLRP Micro Service - device service for connecting LLRP based devices to EdgeX.

## Installation and Execution ##

#### Prerequisites ####

 - Go language
 - GNU Make
 - Docker
 - Docker-compose
 
#### Build ####
```
make build
```

#### Build Docker image ####
```
sudo make docker
```

#### Docker-compose run with other Edgex services (Geneva Release) ####
```
sudo make run
```

#### Docker-compose stop ####
```
sudo make stop
```

## Device Discovery
Upon startup this service will probe the local network 
in an effort to discover devices that support LLRP.

This discovery also happens at a regular interval and can be configured via 
[EdgeX Consul](http://localhost:8500/ui/dc1/kv/edgex/devices/1.0/edgex-device-llrp/Device/Discovery/) 
for existing installations, 
and [configuration.toml][config_toml] for default values.

The local network is probed for every IP on the default LLRP port (`5084`). 
If a device returns LLRP response messages, 
a new EdgeX device is generated under the name format `IP_Port`, like so: `192.0.2.1_1234`.  

### Manually Adding a Device
You can add devices directly via [EdgeX's APIs][add_device]
or via the [toml configuration][config_toml], as in the following example:

```
[[DeviceList]]
  Name = "Speedway"
  Profile = "Device.LLRP.Profile"
  Description = "LLRP RFID Reader"
  Labels = ["LLRP", "RFID"]
  [DeviceList.Protocols]
    [DeviceList.Protocols.tcp]
      host = "192.168.86.88"
      port = "5084"
```

[add_device]: https://app.swaggerhub.com/apis-docs/EdgeXFoundry1/core-metadata/1.2.0#/default/post_v1_device
[config_toml]: cmd/res/configuration.toml

## Connection Management
After an LLRP device is added, either via discovery or directly through EdgeX,
the driver works to maintain a connection to it.
On start-up, it attempts to connect to it.
If it fails to connect or detects that it's lost the connection,
it'll attempt to reconnect repeatedly using exponential backoff with jitter,
capped to a max of 5 mins between attempts.

Because detecting that the connection has dropped requires packet failure,
if more than 2 minutes pass without a successful transfer,
it will reset the connection.
As such, it's useful to configure a Reader 
with a `KeepAliveSpec` with a period less than `120s`. 
This ensures that a healthy connection will not timeout.
You can confirm things are working by unplugging a connected reader.
You should see reconnection attempts logged at the `DEBUG` level
coming from the device service.
Note that it can take up to two minutes before the dropped connection is detected.

These values are not currently configurable,
but they are easy to change before building
within [this code](internal/driver/device.go).
Future work will automatically set up a KeepAlive spec on connection.

## Example Scripts
There are a couple of example scripts here
to interact with devices through EdgeX's APIs.
They aren't meant to be perfect or necessarily the best way to do things,
but they should help give examples of what's possible.
They don't do much error handling, 
so don't rely on them for much more than happy-path testing. 

 - [command][]:
    interacts with the commands service
    to get/set LLRP configs and such.
 - [data][]:
    interacts with the data service to view reports and the like. 
 - [read tags example][read_script]:
    runs a "full" example -- sends/enables [`ROSpec`][ro_spec]
    to the first reader that EdgeX knows about,
    waits a bit, disables/deletes it from the reader,
    then displays any collected tags.
 
They assume everything is running and expect you have a these on your path:
`jq`, `curl`, `sed`, `xargs`, `base64`, and `od`. 
By default, they all try to connect to `localhost` on the typical EdgeX ports.
`command.sh` and `data.sh` take args/options; use `--help` to see their usage.
`example.sh` uses a couple of variables defined at the top of the file
to determine which host/port/file to use.

The [command][] script in particular shows some examples in its `usage`.
You can use it to control arbitrary LLRP configuration,
such as adding/modifying/removing `ROSpec`s and `AccessSpec`s, 
changing a Reader's default `ROAccessReport` reported data,
enabling/modifying/disabling `KeepAlive` messages,
and enabling/disabling specific `ReaderEventNotification`s.

[command]: examples/command.sh
[data]: examples/data.sh
[read_script]: examples/example.sh
[ro_spec]: examples/ROSpec.json

## Testing
There are many unit tests available to run with the typical `go` tools.
`make test` executes `go test ./... -coverprofile=coverage.out` 
and so can be used to quickly run all tests and generate a coverage report.

### LLRP Functional Tests
There are some tests in the `internal/llrp` package 
which expect access to a reader.
By default, they're skipped.
To run them, supply a `-reader=<host>:<port>` argument to Go's test tool.
For example, from the `internal/llrp` directory,
you can run `go test -reader=192.0.2.1:5084`;
assuming an LLRP device is reachable at that IP and port,
it will connect to it, get its config and capabilities,
then try to send and enable/start a basic `ROSpec`.
It waits a short time, possibly collecting `ROAccessReport`s
(assuming tags are in your reader's antennas' FoVs),
and the disables/deletes the `ROSpec`.
The full options it will respond to:

- [`short`](https://golang.org/pkg/testing/#Short) 
    skips the `ROSpec` test described above, 
    since it takes a little while to wait for the reports.
- [`verbose`](https://golang.org/pkg/testing/#Verbose) 
    logs some extra marshaling/unmarshaling data.
- `reader` sets an address of an LLRP device and runs functional tests against it.
- `ro-access-dir` uses a different subdirectory of 
    `internal/llrp/testdata` (by default, `roAccessReports`)
    when running `TestClient_withRecordedData`.
    See below for more info.
- `update` is used in the context of functional tests, 
    but [is only needed in special circumstances](#updating-recorded-test-data)
    and should not be used unless you understand the consequences.
    
Note that if you're using the Goland IDE, 
you can put these options in a test config's `program arguments`,
though the `short` and `verbose` options need the `test.` prefix. 

### Recorded Data Tests
The `internal/llrp/testdata` folder contains a series of `.json` and `.bytes` files.
They're used by [the `TestClient_withRecordedData` unit test][data_tests]
which uses them roughly as follows: 

1. Convert `.json` -> `struct 1` -> `new bytes`
1. Convert `.bytes` -> `struct 2` -> `new JSON`
1. Compare `.json` and `new JSON`
1. Compare `.bytes` and `new bytes`

The test only passes if the unmarshaling/comparisons are successful,
which assumes it's able to match the name to an LLRP message type
(it'll print an error if it doesn't have a match). 
Files in that directory only need to match the format
`{LLRP Message Type}-{3 digits}.{json|bytes}`;
other file names are ignored.
The message name must also be specified in the test's `switch`,
or it'll show an error about not finding a matching message type. 

The test runs the same process using files in `roAccessReports` subdirectory,
which just makes it a little easier to organize those files.
You can use a different test directory with different reports
by using the `ro-access-dir` flag while running that test,
but the alternative directory must be a subdirectory of [`testdata`](internal/llrp/testdata).

For example, to run the test with files in a `testdata/giantReports` directory:

`go test -v -run ^TestClient_withRecordedData$ -ro-access-dir=giantReports`


By default, this directory is set to `roAccessReports`.
If you set it to `""`, it'll skip checking it entirely.

There's actually nothing special about the directory name nor this flag
that requires its contents be `ROAccessReport` message specifically, 
so you're free to segment other json/binary message file pairs
into various directories and rerun the test with appropriate flag values. 
As long as the filenames match the pattern described above
and that name is in the `switch` block of the `compareMessages` function 
of [the test][data_tests] it'll test them.

#### Updating Recorded Test Data
As the test name implies, the `.bytes` data is recorded from an actual reader.
It's possible to use the included Functional tests 
to record new data by passing the `-update` flag.
Under most circumstances, this isn't necessary 
(some cases where it is are described below).

When `-update` is `true`, the [`TestClientFunctional` test][functional_tests] 
skips its normal tests and instead runs the `collectData` function.
That function sends a series of messages to the `-reader` 
and records the binary results in the `testdata` directory,
overwriting existing ones if present (they're version-controlled for a reason).
Most messages are only sent once, hence they'll end in `-000`.
When listening for `ROSpecs`, on the other hand, it'll write as many as it collects.
The [`.gitignore`](.gitignore) is configured to ignore most of them,
but it can be handy for testing.

At present, the recorder ignores the `ro-access-dir` flag described above,
and writes the output directly to the `testdata` directory;
[the data tests](#recorded-data-tests) will happily handle them in `testdata`,
so this can still be fine for testing,
but in the future, it'd probably be better for it to make use of that flag. 

So when is this flag useful?
Basically, if the marshaling/unmarshaling code changes
in a way that results in different JSON/binary interpretation or output.
For the binary side, the format _should_ be fixed,
as its subject to the LLRP specification.
Furthermore, even if the _unmarshaling_ code is wrong,
if the `llrp.Client` code is correctly handling the message boundaries,
the `binary`/`.bytes` files should not have reason to change.

On the other hand, the JSON format is not specific to LLRP.
If the names of keys used in the JSON formats change,
these tests most likely will no longer pass.
For instance, it's possible to implement 
`json.(Unm|M)arshaler`/`encoding.Text(Unm|M)arshaler` interfaces 
to make some LLRP values easier to read.
Doing so may break the `JSON -> Go struct` conversion,
which will result in zero values when going `Go struct -> binary`,
which will change the output. 
In the other direction, the `binary -> Go struct` conversion should be unaffected,
but `Go struct -> JSON` should differ from the recorded value.
The test outputs the location of "first difference" along with some surrounding context
to aid in correcting these tests.
For just name changes, it's probably better to make the changes to the JSON by hand.
However, if the change is large enough, 
it could be easier to just throw away the existing folder and repopulate it.
That is when the `-update` flag makes sense:

`go test -run ^TestClientFunctional$ -reader="$READER_ADDR" -update`

[data_tests]: internal/llrp/reader_data_test.go
[functional_tests]: internal/llrp/reader_functional_test.go

### Test Helpers
There is a [test helper file][test_helper] with some objects/methods
that may be useful when developing unit tests.
The following are particularly useful: 

- `go doc llrp.TestDevice`
- `go doc llrp.GetFunctionalClient`

[test_helper]: internal/llrp/test_helpers.go
