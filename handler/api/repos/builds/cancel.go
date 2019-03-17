// Copyright 2019 Drone IO, Inc.
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

package builds

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/drone/drone/core"
	"github.com/drone/drone/handler/api/render"
	"github.com/drone/drone/logger"
	"github.com/drone/drone/operator/manager"

	"github.com/go-chi/chi"
)

// HandleCancel returns an http.HandlerFunc that processes http
// requests to cancel a pending or running build.
func HandleCancel(
	users core.UserStore,
	repos core.RepositoryStore,
	builds core.BuildStore,
	manager manager.BuildManager,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			namespace = chi.URLParam(r, "owner")
			name      = chi.URLParam(r, "name")
		)

		number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
		if err != nil {
			render.BadRequest(w, err)
			return
		}

		repo, err := repos.FindName(r.Context(), namespace, name)
		if err != nil {
			logger.FromRequest(r).
				WithError(err).
				WithField("namespace", namespace).
				WithField("name", name).
				Debugln("api: cannot find repository")
			render.NotFound(w, err)
			return
		}

		user, err := users.Find(r.Context(), repo.UserID)
		if err != nil {
			logger.FromRequest(r).
				WithError(err).
				WithField("namespace", namespace).
				WithField("name", name).
				Debugln("api: cannot find repository owner")
		}

		build, err := builds.FindNumber(r.Context(), repo.ID, number)
		if err != nil {
			logger.FromRequest(r).
				WithError(err).
				WithField("build", build.Number).
				WithField("namespace", namespace).
				WithField("name", name).
				Debugln("api: cannot find build")
			render.NotFound(w, err)
			return
		}

		if err := manager.Cancel(r.Context(), build, repo, user); err != nil {
			logger.FromRequest(r).
				WithField("build", build.Number).
				WithField("namespace", namespace).
				WithField("name", name).
				Warnf("api: cannot cancel build: %v", err)
			render.InternalError(w, errors.New("Build failed to be cancelled"))
			return
		}

		logger.FromRequest(r).
			WithField("build", build.Number).
			WithField("namespace", namespace).
			WithField("name", name).
			Debugln("api: successfully cancelled build")

		render.JSON(w, build, 200)
	}
}
