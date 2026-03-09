package cli

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func completionCmd() *cli.Command {
	return &cli.Command{
		Name:  "completion",
		Usage: "Generate shell completion scripts",
		Commands: []*cli.Command{
			{
				Name:  "bash",
				Usage: "Generate bash completion script",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					fmt.Print(bashCompletion)
					return nil
				},
			},
			{
				Name:  "zsh",
				Usage: "Generate zsh completion script",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					fmt.Print(zshCompletion)
					return nil
				},
			},
			{
				Name:  "fish",
				Usage: "Generate fish completion script",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					fmt.Print(fishCompletion)
					return nil
				},
			},
		},
	}
}

const bashCompletion = `# seedstorm bash completion
_seedstorm() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local commands="introspect ai-enrich seed generate export version completion"
    COMPREPLY=($(compgen -W "${commands}" -- "${cur}"))
}
complete -F _seedstorm seedstorm
`

const zshCompletion = `#compdef seedstorm
_seedstorm() {
    local -a commands
    commands=(
        'introspect:Discover database schema'
        'ai-enrich:Enrich faker mappings with AI'
        'seed:Seed database with fake data'
        'generate:Generate fake data without inserting'
        'export:Export data to sql/csv/json'
        'version:Print version information'
        'completion:Generate shell completion scripts'
    )
    _describe 'seedstorm commands' commands
}
_seedstorm
`

const fishCompletion = `# seedstorm fish completion
complete -c seedstorm -f
complete -c seedstorm -n '__fish_use_subcommand' -a introspect  -d 'Discover database schema'
complete -c seedstorm -n '__fish_use_subcommand' -a ai-enrich   -d 'Enrich faker mappings with AI'
complete -c seedstorm -n '__fish_use_subcommand' -a seed        -d 'Seed database with fake data'
complete -c seedstorm -n '__fish_use_subcommand' -a generate    -d 'Generate fake data without inserting'
complete -c seedstorm -n '__fish_use_subcommand' -a export      -d 'Export data to sql/csv/json'
complete -c seedstorm -n '__fish_use_subcommand' -a version     -d 'Print version information'
complete -c seedstorm -n '__fish_use_subcommand' -a completion  -d 'Generate shell completion scripts'
`
