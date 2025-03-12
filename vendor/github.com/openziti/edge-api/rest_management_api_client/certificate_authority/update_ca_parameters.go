// Code generated by go-swagger; DO NOT EDIT.

//
// Copyright NetFoundry Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// __          __              _
// \ \        / /             (_)
//  \ \  /\  / /_ _ _ __ _ __  _ _ __   __ _
//   \ \/  \/ / _` | '__| '_ \| | '_ \ / _` |
//    \  /\  / (_| | |  | | | | | | | | (_| | : This file is generated, do not edit it.
//     \/  \/ \__,_|_|  |_| |_|_|_| |_|\__, |
//                                      __/ |
//                                     |___/

package certificate_authority

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"context"
	"net/http"
	"time"

	"github.com/go-openapi/errors"
	"github.com/go-openapi/runtime"
	cr "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"

	"github.com/openziti/edge-api/rest_model"
)

// NewUpdateCaParams creates a new UpdateCaParams object,
// with the default timeout for this client.
//
// Default values are not hydrated, since defaults are normally applied by the API server side.
//
// To enforce default values in parameter, use SetDefaults or WithDefaults.
func NewUpdateCaParams() *UpdateCaParams {
	return &UpdateCaParams{
		timeout: cr.DefaultTimeout,
	}
}

// NewUpdateCaParamsWithTimeout creates a new UpdateCaParams object
// with the ability to set a timeout on a request.
func NewUpdateCaParamsWithTimeout(timeout time.Duration) *UpdateCaParams {
	return &UpdateCaParams{
		timeout: timeout,
	}
}

// NewUpdateCaParamsWithContext creates a new UpdateCaParams object
// with the ability to set a context for a request.
func NewUpdateCaParamsWithContext(ctx context.Context) *UpdateCaParams {
	return &UpdateCaParams{
		Context: ctx,
	}
}

// NewUpdateCaParamsWithHTTPClient creates a new UpdateCaParams object
// with the ability to set a custom HTTPClient for a request.
func NewUpdateCaParamsWithHTTPClient(client *http.Client) *UpdateCaParams {
	return &UpdateCaParams{
		HTTPClient: client,
	}
}

/* UpdateCaParams contains all the parameters to send to the API endpoint
   for the update ca operation.

   Typically these are written to a http.Request.
*/
type UpdateCaParams struct {

	/* Ca.

	   A CA update object
	*/
	Ca *rest_model.CaUpdate

	/* ID.

	   The id of the requested resource
	*/
	ID string

	timeout    time.Duration
	Context    context.Context
	HTTPClient *http.Client
}

// WithDefaults hydrates default values in the update ca params (not the query body).
//
// All values with no default are reset to their zero value.
func (o *UpdateCaParams) WithDefaults() *UpdateCaParams {
	o.SetDefaults()
	return o
}

// SetDefaults hydrates default values in the update ca params (not the query body).
//
// All values with no default are reset to their zero value.
func (o *UpdateCaParams) SetDefaults() {
	// no default values defined for this parameter
}

// WithTimeout adds the timeout to the update ca params
func (o *UpdateCaParams) WithTimeout(timeout time.Duration) *UpdateCaParams {
	o.SetTimeout(timeout)
	return o
}

// SetTimeout adds the timeout to the update ca params
func (o *UpdateCaParams) SetTimeout(timeout time.Duration) {
	o.timeout = timeout
}

// WithContext adds the context to the update ca params
func (o *UpdateCaParams) WithContext(ctx context.Context) *UpdateCaParams {
	o.SetContext(ctx)
	return o
}

// SetContext adds the context to the update ca params
func (o *UpdateCaParams) SetContext(ctx context.Context) {
	o.Context = ctx
}

// WithHTTPClient adds the HTTPClient to the update ca params
func (o *UpdateCaParams) WithHTTPClient(client *http.Client) *UpdateCaParams {
	o.SetHTTPClient(client)
	return o
}

// SetHTTPClient adds the HTTPClient to the update ca params
func (o *UpdateCaParams) SetHTTPClient(client *http.Client) {
	o.HTTPClient = client
}

// WithCa adds the ca to the update ca params
func (o *UpdateCaParams) WithCa(ca *rest_model.CaUpdate) *UpdateCaParams {
	o.SetCa(ca)
	return o
}

// SetCa adds the ca to the update ca params
func (o *UpdateCaParams) SetCa(ca *rest_model.CaUpdate) {
	o.Ca = ca
}

// WithID adds the id to the update ca params
func (o *UpdateCaParams) WithID(id string) *UpdateCaParams {
	o.SetID(id)
	return o
}

// SetID adds the id to the update ca params
func (o *UpdateCaParams) SetID(id string) {
	o.ID = id
}

// WriteToRequest writes these params to a swagger request
func (o *UpdateCaParams) WriteToRequest(r runtime.ClientRequest, reg strfmt.Registry) error {

	if err := r.SetTimeout(o.timeout); err != nil {
		return err
	}
	var res []error
	if o.Ca != nil {
		if err := r.SetBodyParam(o.Ca); err != nil {
			return err
		}
	}

	// path param id
	if err := r.SetPathParam("id", o.ID); err != nil {
		return err
	}

	if len(res) > 0 {
		return errors.CompositeValidationError(res...)
	}
	return nil
}
