// Copyright 2014 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	appTypes "github.com/tsuru/tsuru/types/app"
	"gopkg.in/check.v1"
)

func (s *S) TestPlanAdd(c *check.C) {
	p := appTypes.Plan{
		Name:     "plan1",
		Memory:   9223372036854775807,
		Swap:     1024,
		CpuShare: 100,
	}
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnInsert: func(plan appTypes.Plan) error {
				c.Assert(p, check.Equals, plan)
				return nil
			},
		},
	}
	err := ps.Create(p)
	c.Assert(err, check.IsNil)
}

func (s *S) TestPlanAddInvalid(c *check.C) {
	invalidPlans := []appTypes.Plan{
		{
			Memory:   9223372036854775807,
			Swap:     1024,
			CpuShare: 100,
		},
		{
			Name:     "plan1",
			Memory:   9223372036854775807,
			Swap:     1024,
			CpuShare: 1,
		},
		{
			Name:     "plan1",
			Memory:   4,
			Swap:     1024,
			CpuShare: 100,
		},
	}
	expectedError := []error{appTypes.PlanValidationError{Field: "name"}, appTypes.ErrLimitOfCpuShare, appTypes.ErrLimitOfMemory}
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnInsert: func(appTypes.Plan) error {
				c.Error("storage.Insert should not be called")
				return nil
			},
		},
	}
	for i, p := range invalidPlans {
		err := ps.Create(p)
		c.Assert(err, check.FitsTypeOf, expectedError[i])
	}
}

func (s *S) TestPlansList(c *check.C) {
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnFindAll: func() ([]appTypes.Plan, error) {
				return []appTypes.Plan{
					{Name: "plan1", Memory: 1, Swap: 2, CpuShare: 3},
					{Name: "plan2", Memory: 3, Swap: 4, CpuShare: 5},
				}, nil
			},
		},
	}
	plans, err := ps.List()
	c.Assert(err, check.IsNil)
	c.Assert(plans, check.HasLen, 2)
}

func (s *S) TestPlanRemove(c *check.C) {
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnDelete: func(plan appTypes.Plan) error {
				c.Assert(plan.Name, check.Equals, "Plan1")
				return nil
			},
		},
	}
	err := ps.Remove("Plan1")
	c.Assert(err, check.IsNil)
}

func (s *S) TestDefaultPlan(c *check.C) {
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnFindDefault: func() (*appTypes.Plan, error) {
				return &appTypes.Plan{
					Name:     "default-plan",
					Memory:   1024,
					Swap:     1024,
					CpuShare: 100,
					Default:  true,
				}, nil
			},
		},
	}
	p, err := ps.DefaultPlan()
	c.Assert(err, check.IsNil)
	c.Assert(p.Default, check.Equals, true)
}

func (s *S) TestFindPlanByName(c *check.C) {
	ps := &planService{
		storage: &appTypes.MockPlanStorage{
			OnFindByName: func(name string) (*appTypes.Plan, error) {
				c.Check(name, check.Equals, "plan1")
				return &appTypes.Plan{
					Name:     "plan1",
					Memory:   9223372036854775807,
					Swap:     1024,
					CpuShare: 100,
				}, nil
			},
		},
	}
	plan, err := ps.FindByName("plan1")
	c.Assert(err, check.IsNil)
	c.Assert(plan.Name, check.Equals, "plan1")
}
