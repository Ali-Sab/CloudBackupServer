package com.alison.CloudBackupServer;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class DataController {

    @GetMapping("/")
    public String index() {
        return "My Home Page";
    }
}
