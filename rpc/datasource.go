// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rpc

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/msgpack"
)

// An implementation of packer.Datasource where the data source is actually
// executed over an RPC connection.
type datasource struct {
	commonClient
}

type DatasourceConfigureArgs struct {
	Configs []interface{}
}

type DatasourceConfigureResponse struct {
	Error *BasicError
}

func (d *datasource) Configure(configs ...interface{}) error {
	configs, err := encodeCTYValues(configs)
	if err != nil {
		return err
	}
	var resp DatasourceConfigureResponse
	if err := d.client.Call(d.endpoint+".Configure", &DatasourceConfigureArgs{Configs: configs}, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		err = resp.Error
	}
	return err
}

type OutputSpecResponse struct {
	OutputSpec []byte
}

func (d *datasource) OutputSpec() hcldec.ObjectSpec {
	resp := new(OutputSpecResponse)
	if err := d.client.Call(d.endpoint+".OutputSpec", new(interface{}), resp); err != nil {
		err := fmt.Errorf("Datasource.OutputSpec failed: %v", err)
		panic(err.Error())
	}

	if !d.useProto {
		log.Printf("[DEBUG] - datasource: receiving OutputSpec as gob")
		res := hcldec.ObjectSpec{}
		err := gob.NewDecoder(bytes.NewReader(resp.OutputSpec)).Decode(&res)
		if err != nil {
			panic(fmt.Sprintf("datasource: failed to deserialise HCL spec from gob: %s", err))
		}
		return res
	}

	log.Printf("[DEBUG] - datasource: receiving OutputSpec as gob")
	res, err := protobufToHCL2Spec(resp.OutputSpec)
	if err != nil {
		panic(fmt.Sprintf("datasource: failed to deserialise HCL spec from protobuf: %s", err))
	}
	return res
}

type ExecuteResponse struct {
	Value []byte
	Error *BasicError
}

func (d *datasource) Execute() (cty.Value, error) {
	resp := new(ExecuteResponse)
	if err := d.client.Call(d.endpoint+".Execute", new(interface{}), resp); err != nil {
		err := fmt.Errorf("Datasource.Execute failed: %v", err)
		return cty.NilVal, err
	}

	if !d.useProto {
		log.Printf("[DEBUG] - datasource: receiving Execute as gob")
		res := cty.Value{}
		err := gob.NewDecoder(bytes.NewReader(resp.Value)).Decode(&res)
		if err != nil {
			return res, fmt.Errorf("failed to unmarshal cty.Value from gob blob: %s", err)
		}
		if resp.Error != nil {
			err = resp.Error
		}
		return res, err
	}

	log.Printf("[DEBUG] - datasource: receiving Execute as msgpack")
	res, err := msgpack.Unmarshal(resp.Value, cty.DynamicPseudoType)
	if err != nil {
		return cty.NilVal, fmt.Errorf("failed to unmarshal cty.Value from msgpack blob: %s", err)
	}

	if resp.Error != nil {
		err = resp.Error
	}
	return res, err
}

// DatasourceServer wraps a packer.Datasource implementation and makes it
// exportable as part of a Golang RPC server.
type DatasourceServer struct {
	contextCancel func()

	commonServer
	d packer.Datasource
}

func (d *DatasourceServer) Configure(args *DatasourceConfigureArgs, reply *DatasourceConfigureResponse) error {
	config, err := decodeCTYValues(args.Configs)
	if err != nil {
		return err
	}
	err = d.d.Configure(config...)
	reply.Error = NewBasicError(err)
	return err
}

func (d *DatasourceServer) OutputSpec(args *DatasourceConfigureArgs, reply *OutputSpecResponse) error {
	spec := d.d.OutputSpec()

	if !d.useProto {
		log.Printf("[DEBUG] - datasource: sending OutputSpec as gob")
		b := &bytes.Buffer{}
		err := gob.NewEncoder(b).Encode(spec)
		reply.OutputSpec = b.Bytes()
		return err
	}

	log.Printf("[DEBUG] - datasource: sending OutputSpec as protobuf")
	ret, err := hcl2SpecToProtobuf(spec)
	if err != nil {
		return err
	}
	reply.OutputSpec = ret

	return err
}

func (d *DatasourceServer) Execute(args *interface{}, reply *ExecuteResponse) error {
	spec, err := d.d.Execute()
	reply.Error = NewBasicError(err)

	if !d.useProto {
		log.Printf("[DEBUG] - datasource: sending Execute as gob")
		b := &bytes.Buffer{}
		err = gob.NewEncoder(b).Encode(spec)
		reply.Value = b.Bytes()
		if reply.Error != nil {
			err = reply.Error
		}
		return err
	}

	log.Printf("[DEBUG] - datasource: sending Execute as msgpack")
	raw, err := msgpack.Marshal(spec, cty.DynamicPseudoType)
	reply.Value = raw
	if reply.Error != nil {
		err = reply.Error
	}
	return err
}

func (d *DatasourceServer) Cancel(args *interface{}, reply *interface{}) error {
	if d.contextCancel != nil {
		d.contextCancel()
	}
	return nil
}

func init() {
	gob.Register(new(cty.Value))
}
