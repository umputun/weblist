prep_site:
	cp -fv README.md site/docs/index.md
	sed -i '' 's|https:\/\/github.com\/umputun\/weblist\/raw\/master\/site\/docs\/logo.png|logo.png|' site/docs/index.md
	sed -i '' 's|^.*/workflows/ci.yml.*$$||' site/docs/index.md

.PHONY: prep_site