# Hacker's Guide to LLRP

This document is a minimum need-to-know about LLRP,
where to go next/when you need more info,
what to watch out for,
and the basics of how to use this library effectively.
It can't replace reading [the actual protocol spec][llrp_1_0_1],
but it can get you started.

This doc makes some underlying assumptions about how you're using LLRP
(specifically, you're exchanging binary messages over `tcp/ip`
with an RFID Reader that operates according to the [EPC UHF air protocol][epc_standard]) 
even though _technically_ you could do it other ways
However, if you're in doubt about something this doc says, 
the protocol specification is the authority.

[llrp_1_0_1]: https://www.gs1.org/standards/epc-rfid/llrp/1-0-1
[llrp_1_1]: https://www.gs1.org/standards/epc-rfid/llrp/1-1-0
[epc_standard]: https://www.gs1.org/standards/epc-rfid/uhf-air-interface-protocol

## Basics
In LLRP there's a Client (your computer) and Reader (the RFID device).
They exchange LLRP `Message`s, 
most of which are a Client request followed by a Reader response.

A Client connects to a Reader, 
gives it one or more "Reader Operation Specifications" (`ROSpec`s),
possibly sets up "Access Specifications" (`AccessSpec`s),
and (depending on the config) 
either waits for incoming tag reports,
or polls the Reader for tag reports. 
LLRP also has some stuff to handle general input/output ports,
but this doc isn't going to go into it. 

### Message Structure
LLRP `Message`s are formed of `field`s & `parameter`s;
`Parameter`s are formed of `field`s and sub`parameter`s.
`Field`s are always required in a message, 
though sometimes they have "empty" or default values
or should be ignored.

All communication is encapsulated in `Message`s.
`Field`s are always required, 
but some `Field`s are "lists" which can be empty.
`parameter`s and sub-`Parameter` 
may be required or optional, 
single-use or repeated.

### Minimum Required to get Tag data 
This is the minimum exchange required to get tag data from a Reader;
this makes use of the Reader's current configuration and report specs,
which may or may not be what you want/be useful.
this is just an example, not a recommendation:

- Client connects to a Reader.
- Reader sends a "Connection Successful" event to the Client.
- Client sends the minimal `ROSpec` below.
- Client enables the `ROSpec`;
  it's configured to move immediately from Enabled -> Started. 
- Reader starts the ROSpec; 
  assuming it singulates a tag, it sends tag reports immediately.
  
The minimal ROSpec (as a Go struct supported by our library):

```go
roSpec := llrp.ROSpec{
    ROSpecID: 1,
    ROBoundarySpec: llrp.ROBoundarySpec{
        StartTrigger: llrp.ROSpecStartTrigger{
            Trigger: llrp.ROStartTriggerImmediate,
        },
    },
    AISpecs: []llrp.AISpec{{
        AntennaIDs: []llrp.AntennaID{0},
        InventoryParameterSpecs: []llrp.InventoryParameterSpec{{
            InventoryParameterSpecID: 1,
            AirProtocolID:            llrp.AirProtoEPCGlobalClass1Gen2,
        }},
    }},
    ROReportSpec: &llrp.ROReportSpec{
        Trigger: llrp.NTagsOrAIEnd,
        N:       1,
    },
}
```

and as JSON, as supported by our library 
(replaces the const variables with their LLRP values)

```json
{
    "ROSpecID": 1,
    "ROBoundarySpec": {
        "StartTrigger": { "Trigger": 1 }
    },
    "AISpecs": [{
        "AntennaIDs": [0],
        "InventoryParameterSpecs": [{
            "InventoryParameterSpecID": 1,
            "AirProtocolID":            1 
        }]
    }],
    "ROReportSpec": {
        "Trigger": 1, 
        "N":       1
    }
}
```

- By using an `Immediate` `StartTrigger`, 
  sending `EnableROSpec` is enough to start it.
- Setting `AntennaIDs` to `[0]` targets all antennas.
- Setting the `ROReportSpec` to `NTagsOrAIEnd` with `N` equal to 1
  tells the Reader to send us an `ROAccessReport` for every tag it reads.
  
You can use the `llrp` binary in the `cmd` directory 
as a command line utility to send arbitrary `ROSpec`s to a Reader
and listen for incoming `ROAccessReport` messages.
You can get its `usage` via the `-help` flag.

### Exceptions to Client request/Reader response
- The Reader can send the Client async events and reports;
  the Client doesn't respond to these messages.
- The Client acknowledges Keep Alive messages from the Reader.
- Some Readers allow configuring a "ClientRequestOpSpec", which is basically
  "I read this tag; what do you want to do?", and the Client responds.
- Custom messages can basically do anything; 
    we just treat the content as binary blobs (base64 encoded in JSON) 
    and it's up to other layers with more specific knowledge to deal with them. 
- Technically, LLRP requires Clients be capable of accepting Reader connections,
  even though they can choose not to do so; we do not accept Reader connections. 
  
### Mapping Identifiers between LLRP and our Library
To interact with our library via Go or JSON,
you'll need to know the parameter names and structures.
For the most part, the library uses typical LLRP parameter and message names,
but this is not universally true:
in some cases, LLRP parameter names are not valid Go identifiers,
and in other cases they are simply too unwieldy for reasonable, ergonomic use. 

Our LLRP library uses the Go standard library JSON marshaling/unmarshaling,
so you need only to know these structures' names and exported fields,
which can be found simply by perusing [the code](generated_structs.go)
or using `go doc llrp`.

In the Go library, we've given identifiers to many LLRP enumerations;
in JSON, these constants are simply their LLRP equivalent:
e.g., in LLRP `Periodic` `KeepAliveTrigger` has the value `1`,
so in Go, you can write `Trigger: llrp.KATriggerPeriodic` 
whereas in JSON, you'd use `Trigger: 1`.

## Navigating the Specification
There are (sort of) two versions of LLRP:
- [`1.0.1`][llrp_1_0_1] (aka version `1`) was published in 2007.
- [`1.1`][llrp_1_1] (aka version `2`) was published in 2010.

`1.1` only adds a couple of things, 
and technically still says "draft", even though the standards body 
(previously EPCglobal, now GS1) publishes it on their website as "the latest version".
Adoption varies among Reader manufacturers
(e.g., Impinj Readers support 1.0.1, while Alien Readers support both),
but our library handles version negotiation and all the standard messages.
We don't validate that a given message is supported by a given version,
as compliant Readers should reject invalid messages anyway.

You may need to reference the [EPC standard][epc_standard]
to fully understand all the parameter definitions.
It is enormous and has been updated more frequently/recently than LLRP;
it's not required reading to understand LLRP,
but you do need to understand it to make the best use of all LLRP's parameters.

The major chunks of the doc:
- Chapters 1-4 are basic definitions & intro material.
- Chapters 5 & 6 describe the general idea/goals/process of LLRP,
  and you should absolutely read them -- 
  it's 11 pages including drawings.
  The `1.1` state transition diagrams are more clear.
- Chapters 8-15 (16 in `1.1`) describe the "abstract" message format,
  while Chapter 16 (17 in `1.1`) gives the binary format.
- The rest of the doc is just helpful information,
  like where to find other specification docs.
  The `1.0.1` doc includes some UML drawings of questionable value.
 
The LLRP manual makes a big distinction 
between the "abstract" message format and the binary format,
but in reality they are tightly coupled.
You should open two copies of the spec 
and compare the message formats side-by-side, 
as it will make their structures easier to understand.
  
> Important: The "abstract" format often presents fields/parameters
> in an order different from the binary format.
> Some parts of the abstract format allow for more flexibility
> than the binary format permits.

### Air Protocols
The standard was very forward-thinking in terms of 
"how can we handle changes in RFID tech without major changes to this?".
The solution they used is that you can ask a Reader,
Parts of the spec say "These bits depend on the Air Protocol",
and then there's a section dedicated to Air-Protocol-Specific parameters.
But after 13 years, there's only been 1 Air Protocol in the standard,
the "EPCglobal Class-1 Generation-2 UHF RFID Protocol",
helpfully abbreviated `C1G2`.
As a result, this library ignores that particular abstraction
and instead directly inserts the C1G2 parameters
as if they're the only ones allowed, because in practice, they are.

## Parsing Binary LLRP Messages 
You will only need to deal with the binary message form 
if you need to modify the library 
or to add support for a custom `message`/`parameter`.
Also note that most of the parser code 
is generated by [a python script][gen_script] 
from [a yaml definition][yaml_def] of the messages.
It can probably be adopted for other purposes.

[gen_script]: generate_param_code.py
[yaml_def]: messages.yaml

Since the details of the binary protocol are pretty well specified,
this is only a high-level overview.
Binary or not, all the communication in LLRP is "contained" in `Message`s,
which are made up of `Field`s and `Parameter`s.
`Field`s are basic values (think `uint`s, `bool`s, arrays, and `string`s)
while `Parameter`s are containers holding `Field`s and sub-`Parameter`s.

Each binary `Message` has a short header 
identifying the LLRP version, the message type, and its total byte length.
After that is the message payload, if present.
`Parameter`s, like `Message`s, have a header 
identifying their type and usually their byte length
(some `Parameter` types have a fixed length,
and so they don't include it in the header).

`Field`s don't have a header -- 
you know what `Field`s to expect 
based on the `Message`/`Parameter` type.
Most `Field`s have a fixed size,
but some `Field`s are lists of fixed-size types;
they start with a `uint16` list size, which may be zero.

If the list element type size is one byte, 
then length is the number of bytes to follow;
if the list element type size is 2 bytes,
then the next 2x that value bytes make up the list.
There are two special cases: 
- `string` fields always UTF-8 encoded, 
    and their size header gives the number of bytes in the string.
- `bit` fields are MSB-aligned and padded to octet boundaries,
    and their size header gives the number of bits,
    so you need to round it up to the nearest multiple of 8
    to determine how many of the next bytes make up the bit array.
    Unlike other types which can be converted directly into Go slices,
    you need to store the field length 
    (so you know whether or not the final byte is partial).
    
The generated parser code handles lists & fields for you.
It determines how to parse the message based on the YAML definition.
Types that begin with `[]` are interpreted as lists.


## Problems with the Spec
Portions of the LLRP specification are confusing, contradictory, or confounding.
This is an incomplete list of things to watch out for.
This isn't an "LLRP sucks" list; only a few are outright errors.
Most of these are only issues for "general purpose" LLRP processing,
but are fine when given context outside the scope of LLRP communication.

### Problems with the Mode Table
There are several inconsistencies in the `UFHC1G2` parameters,
which is unfortunate, since they're the primarily LLRP controls for a Reader.
There material differences between the abstract and binary parameter definitions,
as well as inconsistencies between how `Capabilities` are presented by the Reader 
and how the Client references those values in `Spec` parameters. 

### Inconsistent ModeID/ModeIndex Definitions
In the abstract definitions,
the `C1G2RFControl` parameter (335) defines `ModeIndex` as `Unsigned Integer`
and says "This is an index into the UHFC1G2RFModeTable";
the "Formatting Conventions and Data Types" section of the spec
states that `Unsigned Integer` takes values between 0 and 2^32-1,
(as would a `uint32`), but the binary definition allocates it only 16 bits.
The `UHFC1G2RFModeTableEntry` parameter (329) defines `Mode identifier`,
also specified as an `Unsigned Integer`, with the text 
"a Reader defined identifier that 
the client may use to set the Gen2 operating parameters";
its binary definition is allocated 32 bits.
We assume that `ModeIndex` and `Mode identifier` are meant to reference the same thing,
and further assume that Readers will simply limit their identifiers to 2^16-1. 
An unfortunate consequence is that you must cast RFModeIDs from uint16 to uint32
if taken directly from the UHF Mode Table and inserted into an RFControl parameter.

### Inconsistent Tari Binary Definitions
This is similar to the problem above.
The `UHFC1G2RFModeTableEntry` parameter (329) defines 3 `Tari` values as `Integer`,
restricted to 6250-25000 (an EPC limitation), which fits in a `uint16`;
their binary definition allocates them each 32 bits. 
The `C1G2RFControl` parameter (335) also defines `Tari` as `Integer` in 6250-25000 (or 0),
and its binary allocates it 16 bits. 
Since the limits are fine for that restriction (even assuming proper 2-complement),
it's only an annoying consequence that the types are not directly compatible,
and one must be cast to the other.

### Inconsistent HopTableID Definitions
In the abstract definitions,
the `RFTransmitter` parameter (224) defines `HopTableID` as `Unsigned Short Integer`, 
while the `FrequencyHopTable` parameter (147) defines it as `Integer`,
but further specifies `Possible Values: 0-255`, 
effectively restricting it to a `uint8`.

The `RFTransmitter` binary definition gives the `HopTableID` 16 bits,
consistent with its abstract definition.
The `FrequencyHopTable` gives the `HopTableID` 8 bits,
followed by an 8-bit `Reserved` section,
presumably for the possibility to match the `RFTransmitter` in the future,
though doing so would be backwards-incompatible 
since the reserved bits come _after_ the `HopTableID`, 
not before as they would if it were a (big-endian) `uint16`.
_Technically_, because the abstract definition says its an `Integer`,
it binary format must be encoded twos-complement, 
and thus the `Possible Values: 0-255` requires 9 bits (one of which must always be 0),
meaning the binary definition does not give enough space for all required values.

Our parser uses `uint8` for the `FrequencyHopTable.HopTableID` 
and `uint16` for the `RFTransmitter.HopTableID`, 
which is always correct for legal values 
(the Reader has no legal way to send a `HopTableID` > 127) 
and probably matches the intent if a Reader tries to use `HopTableID`s 128-255.

### Inconsistent Parameter Ordering
Parameters in the abstract part of the spec 
are sometimes listed in a different order than in the binary specification.

### Inconsistent Parameter Naming 
Parameters in the abstract spec sometimes have different names
than those used in the binary spec.
The most egregious example 

### Inconsistent Parameter Naming 
Most of the binary spec requires parameters appear in a specific order.
Because many parameters are optional,
a _correct_ parser would validate the parameter order.
When multiple, optional parameters occur in the binary protocol in sequence,
our parser allows them in any order, for the following reasons: 

- It's more efficient and far easier to implement.
- It has no effect on valid messages (i.e., it'd never be rejected).
- An invalid message is _probably fine_ to accept with an "out of order" parameter;
   if a Reader produces parameters out of order, it's more reasonable to be flexible. 

Very occasionally, the parameter order does have special meaning:

- The execution order of `AISpec`s, `RFSurveySpec`s, and `CustomSpec`s 
    within an `ROSpec` is based on their definition order.
- The abstract definition allows multiple events in the same `ReaderEventNotification`,
    and states their order must match the order in which they occurred.
    
Our library does not permit specifying the order of parameters of different types;
this is primarily to make it easier to work with Go 
(heterogeneous slices are not permitted, 
so they'd have to be abstracted behind an interface).

For event ordering, this makes no difference: 
despite the abstract requirement, 
the binary spec _does_ enforce a particular ordering.

Our implementation always serializes `ROSpec`s inner specs in this order:
- each `AISpec`, in order
- each `RFSurveySpec`, in order
- each `CustomSpec`, in order
- `LoopSpec` (valid only for LLRP version >=1.1)

### 
