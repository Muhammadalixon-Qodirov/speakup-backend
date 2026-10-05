package services

// Part2Question has a main question + bullet point prompts.
type Part2Question struct {
	Question string   `json:"question"`
	Prompts  []string `json:"prompts"`
}

// IELTSTopic holds all parts for one topic.
type IELTSTopic struct {
	ID    string          `json:"id"`
	Part1 []string        `json:"part1"`
	Part2 []Part2Question `json:"part2"`
	Part3 []string        `json:"part3"`
}

// IELTSCategory groups topics under a name + icon.
type IELTSCategory struct {
	Name   string       `json:"name"`
	Icon   string       `json:"icon"`
	Topics []IELTSTopic `json:"topics"`
}

// IELTSTopicsResponse is what GET /ai/topics returns.
type IELTSTopicsResponse struct {
	Categories []IELTSCategory `json:"categories"`
}

// GetSpeakingTopics returns all IELTS speaking topics with Part 1/2/3.
func GetSpeakingTopics() IELTSTopicsResponse {
	return IELTSTopicsResponse{Categories: allTopics}
}

var allTopics = []IELTSCategory{
	{Name: "Animals", Icon: "🐾", Topics: []IELTSTopic{{ID: "animals",
		Part1: []string{"Do you have a pet?", "What is a popular pet in your country?", "What problems do people have with pets?", "Did you have a pet as a child?", "Have you ever seen a wild animal?", "Are there many wild animals in your country?"},
		Part2: []Part2Question{{Question: "Describe an interesting animal", Prompts: []string{"What it is", "Where it lives", "Where you first saw it", "Why it's interesting"}}},
		Part3: []string{"How can the elderly benefit from having a pet?", "Should the government protect wild animals?", "Why are some animals endangered?", "What are the advantages and disadvantages of zoos?"},
	}}},
	{Name: "Art", Icon: "🎨", Topics: []IELTSTopic{{ID: "art",
		Part1: []string{"What kind of art do you enjoy?", "Do you have any paintings or pictures on your walls at home?", "Did you learn art at school?", "Can you draw or paint?", "Do you like visiting museums or art galleries?", "Do you often take photos?", "Do you often take selfies?"},
		Part2: []Part2Question{
			{Question: "Describe a work of art you like", Prompts: []string{"What it is", "Where you saw it", "What it shows", "Why you like it"}},
			{Question: "Describe a museum or gallery you have visited", Prompts: []string{"What it is", "Where it is located", "What can be seen there", "Why you liked it"}},
			{Question: "Describe someone creative you know", Prompts: []string{"Who it is", "How you know them", "How they are creative", "Why you enjoy knowing this person"}},
		},
		Part3: []string{"What role does art play in society?", "Do you think photos will one day replace paintings?", "Do you think people today are less creative than in the past?", "Why do people take selfies?", "What role do museums and galleries play?", "How can children benefit from art?", "Should the government support the arts?", "What makes a good painting?", "Should art be censored?"},
	}}},
	{Name: "Apps & Technology", Icon: "📱", Topics: []IELTSTopic{{ID: "apps",
		Part1: []string{"Do you often use apps?", "What are the most popular apps in your country?", "Are there any useful apps for children?", "Would you ever spend money on an app?", "Have you ever deleted an app?"},
		Part2: []Part2Question{{Question: "Describe a useful app", Prompts: []string{"What it is", "How you heard about it", "What it does", "Why you find it useful"}}},
		Part3: []string{"Are apps useful or are they a distraction?", "How do people stop themselves getting distracted by apps?", "Why do old people struggle with apps?", "How do you think apps will develop in the future?", "Can apps help us improve our health?", "What is the downside of using apps for educational purposes?"},
	}}},
	{Name: "Books", Icon: "📚", Topics: []IELTSTopic{{ID: "books",
		Part1: []string{"Do you like reading books?", "Do you ever read e-books?", "Did you read much as a child?", "What children's story is popular in your country?", "What type of books are most popular in your country?"},
		Part2: []Part2Question{
			{Question: "Describe a book you recently read", Prompts: []string{"What type of book it is", "What it is about", "Why you enjoyed it"}},
			{Question: "Describe a book that was made into a film", Prompts: []string{"What it is", "What the plot was", "What the film was like", "Why you enjoyed it"}},
			{Question: "Describe a famous author", Prompts: []string{"Who it is", "What you know about them", "What they wrote", "Why you like them"}},
		},
		Part3: []string{"Do you think paper books will ever disappear?", "What do children gain from reading books?", "How can children be encouraged to read more?", "Do men and women like reading similar types of books?", "Why are films often different from the book they are based on?", "What makes a novel successful?"},
	}}},
	{Name: "Challenges", Icon: "💪", Topics: []IELTSTopic{{ID: "challenges",
		Part1: []string{"When was the last time you tried something new?", "Do you enjoy stepping out of your comfort zone?", "What did you find most challenging at school?", "Do you think children should be challenged more at school?", "When was the last time you found something too difficult?"},
		Part2: []Part2Question{
			{Question: "Describe a tough challenge you have faced", Prompts: []string{"What was it", "When did it happen", "How did you cope", "Why do you remember it"}},
			{Question: "Describe someone who is adventurous", Prompts: []string{"Who it is", "How you know them", "What they enjoy doing", "Why you consider them adventurous"}},
			{Question: "Describe a job you think is challenging", Prompts: []string{"What it is", "What it involves", "Who does it", "Why you think it is challenging"}},
		},
		Part3: []string{"Why do some people relish a challenge?", "Are challenges good for us?", "Why do some people avoid challenges?", "Do you think some people are born more adventurous?", "Can life sometimes be too easy?", "Should we protect children from difficult situations?", "What are the greatest challenges people face today?"},
	}}},
	{Name: "Clothes & Fashion", Icon: "👗", Topics: []IELTSTopic{{ID: "clothes",
		Part1: []string{"What types of clothes do you wear most?", "When was the last time you bought an item of clothing?", "Why do some people prefer casual clothes?", "Did you wear a school uniform as a child?", "Do you follow fashion?", "Do you have any traditional clothes?", "Would you ever spend a lot of money on clothes?"},
		Part2: []Part2Question{
			{Question: "Describe an item of clothing you most enjoy wearing", Prompts: []string{"What it is", "How often you wear it", "When you bought it", "Why you enjoy wearing it"}},
			{Question: "Describe someone with a good sense of fashion", Prompts: []string{"Who it is", "What they are like", "What they enjoy wearing", "Why you think they are fashionable"}},
		},
		Part3: []string{"Do men enjoy fashion as much as women?", "Do you think fashion is important?", "Can you tell a lot about a person from what they wear?", "Do you think traditional clothes will one day disappear?", "Do you think social media influences what we wear?", "How have clothing trends changed over the last few decades?", "How do movie stars impact fashion?"},
	}}},
	{Name: "Confidence", Icon: "💎", Topics: []IELTSTopic{{ID: "confidence",
		Part1: []string{"Would you describe yourself as a confident person?", "Were you a confident child?", "What made you nervous as a child at school?", "Can you tell by looking at someone whether they are confident?", "What do you do to help you build confidence?"},
		Part2: []Part2Question{
			{Question: "Describe a person you know who is confident", Prompts: []string{"Who they are", "How you know them", "What they are like", "Why you think they are confident"}},
			{Question: "Describe a time you felt confident or lost confidence", Prompts: []string{"When it was", "Where you were", "What happened", "Why you felt that way"}},
		},
		Part3: []string{"Why is confidence important?", "How can people develop confidence?", "Do you think some people are born naturally confident?", "Can someone ever be over-confident?", "Do you think social media makes people more or less confident?", "Why do women often struggle to feel confident about their looks?"},
	}}},
	{Name: "Crime & Rules", Icon: "⚖️", Topics: []IELTSTopic{{ID: "crime",
		Part1: []string{"What is the biggest mistake you've ever made?", "Did your school have strict rules?", "Were teachers strict at your school?", "What was the usual punishment if you misbehaved?", "Is there a lot of crime where you live?"},
		Part2: []Part2Question{
			{Question: "Describe a good law", Prompts: []string{"What it is", "When it was introduced", "How it impacts people", "Why you think it is good"}},
			{Question: "Describe a time you made a mistake", Prompts: []string{"When it was", "Who was there", "What happened", "How you felt"}},
		},
		Part3: []string{"Should parents enforce strict rules on teenage children?", "Who should teach children right from wrong?", "Why do people commit crime?", "Is prison the best punishment?", "How can we reduce crime in society?", "Do you think online crime is a big problem?", "How can people stay safe online?"},
	}}},
	{Name: "Education", Icon: "🎓", Topics: []IELTSTopic{{ID: "education",
		Part1: []string{"Did you enjoy school as a child?", "What was your favourite subject?", "Was there any subject you didn't like?", "Did you have a favourite teacher at school?", "Did you ever do any extra-curricular activities?", "Are you currently learning anything new?", "Do you ever use educational apps?"},
		Part2: []Part2Question{
			{Question: "Describe a subject you enjoyed at school", Prompts: []string{"What it was", "Who taught you", "What you learned", "Why you enjoyed it"}},
			{Question: "Describe a teacher you admired", Prompts: []string{"Who it was", "What they taught", "What you enjoyed about their lessons", "Why you admired them"}},
			{Question: "Describe something you wish you had learned", Prompts: []string{"What it was", "How you hoped to learn it", "How it could have helped you", "Why you didn't learn it"}},
		},
		Part3: []string{"Do you think education has changed a lot in the last few decades?", "Do you think lessons should be fun or just educational?", "How could teachers improve their lessons?", "Do you think parents sometimes pressure children to learn too much?", "What makes a good teacher?", "How has technology changed education?", "What are the advantages and disadvantages of online learning?"},
	}}},
	{Name: "Friends & Family", Icon: "👨‍👩‍👧‍👦", Topics: []IELTSTopic{{ID: "family",
		Part1: []string{"Who are you closest to in your family?", "Do you have a big family?", "Were your parents strict when you were a child?", "Do you think family is important?", "Do you have many friends?", "What do you enjoy doing with your friends?", "When was the last time you made a new friend?"},
		Part2: []Part2Question{
			{Question: "Describe your best friend", Prompts: []string{"Who it is", "What they are like", "What you did together", "Why you like them"}},
			{Question: "Describe a memorable family holiday", Prompts: []string{"Where it was", "Who you went with", "What you did there", "Why it was memorable"}},
			{Question: "Describe a time when a friend helped you", Prompts: []string{"When was it", "Who your friend is", "What they did", "Why it was helpful"}},
		},
		Part3: []string{"Why is family important?", "Do you think grandparents still have a role to play?", "Should parents be strict?", "Who should be responsible for the care of elderly people?", "How have families changed over the last few decades?", "What characteristics are important in a friend?", "Is it safe to make new friends online?"},
	}}},
	{Name: "Food", Icon: "🍽️", Topics: []IELTSTopic{{ID: "food",
		Part1: []string{"What's your favourite meal of the day?", "Is there any food you didn't like as a child that you do now?", "Do you enjoy cooking?", "How often do you cook?", "Do you often eat out?", "When was the last time you tried a new dish?", "Do you like spicy foods?"},
		Part2: []Part2Question{
			{Question: "Describe a foreign food you would like to try", Prompts: []string{"What it is", "Where it comes from", "How you heard of it", "Why you would like to try it"}},
			{Question: "Describe a restaurant you like", Prompts: []string{"What type of food it serves", "What it is like", "What dishes you enjoy", "Why you like it"}},
			{Question: "Describe your favourite dish", Prompts: []string{"What it is", "How often you have it", "How it is prepared", "Why you enjoy it"}},
		},
		Part3: []string{"Do you think food plays an important role in society?", "How has popular food changed in your country?", "Should the government close down fast food restaurants?", "Why is obesity a growing problem?", "What is a balanced diet?", "Do you think family members should eat together?", "Do you think people eat more healthily now or in the past?"},
	}}},
	{Name: "Happiness", Icon: "😊", Topics: []IELTSTopic{{ID: "happiness",
		Part1: []string{"Do you consider yourself a happy person?", "What did you enjoy doing as a child?", "What makes you happy?", "Do you think humour is important?", "Do you often laugh out loud?"},
		Part2: []Part2Question{
			{Question: "Describe a happy event you remember", Prompts: []string{"When it was", "Who was with you", "What you did", "Why it was a happy event"}},
			{Question: "Describe an activity that makes you happy", Prompts: []string{"What it is", "Where you do it", "What you need to do it", "Why it makes you happy"}},
		},
		Part3: []string{"Do you think successful people are happier?", "Do you think money can contribute towards happiness?", "What period in life is the happiest?", "Do you think people are happier now than in the past?", "Are children happier than adults?", "How can elderly people find happiness late in life?"},
	}}},
	{Name: "Health", Icon: "🏥", Topics: []IELTSTopic{{ID: "health",
		Part1: []string{"How do you keep healthy?", "Do you have a healthy lifestyle?", "What exercises are most popular in your country?", "Did you ever play sport at school?", "Do you ever do exercise?", "What do you think is a healthy daily routine?", "Do you think people are healthier now than in the past?"},
		Part2: []Part2Question{
			{Question: "Describe a healthy activity", Prompts: []string{"What it is", "How it is done", "Who can enjoy it", "Why it is healthy"}},
			{Question: "Describe someone you think is very healthy", Prompts: []string{"Who it is", "How you know them", "What healthy activities they do", "How you feel about them"}},
		},
		Part3: []string{"What health problems do many people face today?", "Are people more conscious about their health now?", "How can people be encouraged to be healthier?", "Do you think fast food is responsible for bad health?", "Do you think mental health is important?", "What do you think impacts our mental health?", "Do you think modern technology is making people less healthy?"},
	}}},
	{Name: "Holidays & Travel", Icon: "✈️", Topics: []IELTSTopic{{ID: "holidays",
		Part1: []string{"Did you often go on holiday as a child?", "What activities did you enjoy on holiday?", "Do you think holidays are important?", "What holiday destinations are popular in your country?", "When did you last go on holiday?", "If you could go anywhere in the world, where would you go?"},
		Part2: []Part2Question{
			{Question: "Describe a recent holiday you had", Prompts: []string{"Where it was", "Who went with you", "What you did there", "Why you enjoyed it"}},
			{Question: "Describe a holiday that didn't go as planned", Prompts: []string{"Where you went", "What happened", "How it got resolved", "Why you remember it"}},
			{Question: "Describe a city you would like to visit", Prompts: []string{"Where it is", "What it is like", "What you would want to do", "Why you would enjoy it"}},
		},
		Part3: []string{"How do you think holidays will change in the future?", "Do you think adventure holidays are good for all types of people?", "What are the pros and cons of tourism for a country?", "Do you think tourism is good for historical places?", "Why do people want to travel abroad?", "Does tourism cause pollution due to mass travel?"},
	}}},
	{Name: "Media & Films", Icon: "🎬", Topics: []IELTSTopic{{ID: "media",
		Part1: []string{"Do you often watch films?", "Do people in your country often go to the cinema?", "How do you keep up with the news?", "Do you ever just skim the headlines?", "Do you often post on social media?", "What social media sites do you use most?", "Do you think social media is bringing people closer?"},
		Part2: []Part2Question{
			{Question: "Describe your favourite film", Prompts: []string{"What it is", "When you first watched it", "What it is about", "Why you like it so much"}},
			{Question: "Describe a person in the news you would like to meet", Prompts: []string{"Who it is", "When you first heard about them", "Why you would like to meet them"}},
			{Question: "Describe your favourite social media site or app", Prompts: []string{"What it is", "How often you use it", "What you use it for", "Why you like it"}},
		},
		Part3: []string{"Do you think all news is true?", "Should people be skeptical of news online?", "How has social media changed the way we receive news?", "Why do people enjoy watching films or drama series?", "Do you think historical dramas should be factually correct?", "Should parents control what their children watch?", "Do old and young people like watching the same things?"},
	}}},
	{Name: "Nature & Environment", Icon: "🌿", Topics: []IELTSTopic{{ID: "nature",
		Part1: []string{"Do you spend much time in nature?", "Do you have many plants in your home?", "Have you ever grown vegetables?", "Are there many parks where you live?", "Did you ever learn about the environment at school?", "Do you think children should spend more time outside?", "Do you ever recycle?"},
		Part2: []Part2Question{
			{Question: "Describe a place of natural beauty in your country", Prompts: []string{"Where it is", "When you first went there", "What there is to see", "Why it is special"}},
			{Question: "Describe someone who is environmentally friendly", Prompts: []string{"Who it is", "How you know them", "What they do to protect the environment"}},
			{Question: "Describe an outdoor activity you enjoy", Prompts: []string{"What it is", "Where you do it", "How you do it", "Why you enjoy it"}},
		},
		Part3: []string{"Should the government do more to protect rural areas?", "How do people damage our planet?", "What can individuals do to help the environment?", "Should children be more involved in protecting the environment?", "Do you think cities should build more green spaces?", "Why is urban planning important?"},
	}}},
	{Name: "Politeness", Icon: "🤝", Topics: []IELTSTopic{{ID: "politeness",
		Part1: []string{"How do people show politeness in your country?", "Are children encouraged to be polite to older people?", "Was it your teachers or parents who taught you to be polite?", "Do you think people are less polite than in the past?", "Why are people sometimes rude?", "Do you ever lose your patience?"},
		Part2: []Part2Question{
			{Question: "Describe someone you know who is very polite", Prompts: []string{"Who it is", "What they are like", "How you know them", "Why they are so polite"}},
			{Question: "Describe a time you lost your patience", Prompts: []string{"When was it", "Who were you with", "What happened", "What you did afterwards"}},
		},
		Part3: []string{"Why are people polite to strangers?", "Should children be taught good manners?", "Is politeness less important today than in the past?", "Why do people often lose their patience?", "What jobs require politeness?", "Can people be both assertive and polite?"},
	}}},
	{Name: "Success & Work", Icon: "🏆", Topics: []IELTSTopic{{ID: "success",
		Part1: []string{"Do you consider yourself successful?", "What does success mean to you?", "Who is the most successful person you know?", "Do you think hard work always leads to success?", "What skills are important to succeed in life?"},
		Part2: []Part2Question{
			{Question: "Describe someone who is successful", Prompts: []string{"Who it is", "What they are like", "What they do", "Why they are successful"}},
			{Question: "Describe an achievement you are proud of", Prompts: []string{"What it is", "When you did it", "What it involved", "Why you felt proud"}},
			{Question: "Describe a job you think is important", Prompts: []string{"What it is", "Who usually does this work", "What is involved", "Why it is important"}},
		},
		Part3: []string{"Why are some people more successful than others?", "Do you think success is innate?", "What makes a good leader?", "Do you think charisma is important in a leader?", "What makes a company successful?", "How has technology changed how we work?", "Do you think satisfaction or money is more important?"},
	}}},
	{Name: "Weather", Icon: "🌤️", Topics: []IELTSTopic{{ID: "weather",
		Part1: []string{"What is your favourite season?", "What weather do you enjoy most?", "Is there any type of weather you don't like?", "Does the weather affect your mood?", "Do you like rainy days?", "How many seasons are there in your country?", "Does the weather ever affect transport in your country?"},
		Part2: []Part2Question{
			{Question: "Describe an activity you enjoy in warm weather", Prompts: []string{"What it is", "Where you do it", "How it is done", "Why you enjoy it"}},
			{Question: "Describe a day when the weather was perfect", Prompts: []string{"When it was", "What it was like", "What you did", "Why you enjoyed it"}},
			{Question: "Describe your favourite season", Prompts: []string{"When it is", "What it is like", "What you do during that season", "Why you like it"}},
		},
		Part3: []string{"What type of climate is most healthy?", "How does climate affect lifestyle choices?", "Do you ever have extreme weather in your country?", "How can extreme weather impact a country?", "What is climate change?", "Do you think climate change is a serious problem?", "What impact does weather have on farming?"},
	}}},
	{Name: "Remembering", Icon: "🧠", Topics: []IELTSTopic{{ID: "remembering",
		Part1: []string{"Do you have a good memory?", "Do you often forget things?", "Why are some people so forgetful?", "Have you ever kept a diary?", "Do you use calendars?", "What can people do to improve their memory?"},
		Part2: []Part2Question{
			{Question: "Describe a memorable experience", Prompts: []string{"When it was", "What happened", "Why it is so memorable"}},
			{Question: "Describe a childhood memory", Prompts: []string{"When it was", "Who was involved", "What happened", "Why it is so memorable"}},
		},
		Part3: []string{"Why do older people struggle with their memory?", "Why do some people have better memories than others?", "What can people do to improve their memory?", "Should children be taught to memorise things?", "Is it important to learn the history of one's country?", "Does technology help us remember or make it harder?"},
	}}},
	{Name: "Sugar & Food Habits", Icon: "🍫", Topics: []IELTSTopic{{ID: "sugar",
		Part1: []string{"Do you have a sweet tooth?", "How often do you eat sugary foods?", "What are some popular sweets in your country?", "Do people often eat chocolate in your country?", "Did you eat many sweets as a child?", "Do you think any sweet food is healthy?"},
		Part2: []Part2Question{{Question: "Describe a sweet treat you enjoy", Prompts: []string{"What it is", "How often you eat it", "What is special about it", "Why you like it"}}},
		Part3: []string{"Why do people eat food they know isn't good for them?", "Should parents be stricter about what their children eat?", "Why do people eat so many sugary foods?", "How can the problem of obesity be tackled?"},
	}}},
}
