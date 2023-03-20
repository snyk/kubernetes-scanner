/*
 * © 2023 Snyk Limited
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package github

import "testing"

func TestRepoURL(t *testing.T) {
	url := (&RepositoryClient{
		repoName:  "kubernetes-scanner",
		repoOwner: "snyk",
	}).RepoURL()
	if url != "https://github.com/snyk/kubernetes-scanner.git" {
		t.Fatalf("wrong github URL: %v", url)
	}
}
